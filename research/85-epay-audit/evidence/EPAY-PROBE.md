# EPAY_PROBE — AJS-324

**Run:** 2026-10-25 21:41 JST on HMC (read-only)
**Gateway host:** `dash.natriumgroup.com` (HTTPS, port 443; resolves to 163.181.214.1)
**Credential source:** `/srv/dwgxhomecloud/scratch/ajs-deploy-20261024/daemon/.env` — `AJS_EPAY_GATEWAY`, `AJS_EPAY_PID` and `AJS_EPAY_KEY` were read into shell variables; none of their values were echoed.

## Request

`GET https://dash.natriumgroup.com/api.php` with the `act=order`, `pid`, `key` and `out_trade_no=apc-probe-nonexistent` parameters. The request was sent with curl using a stdin config, so the key and URL query never appeared in process arguments or output. `submit.php` was not called and no order was created.

## Result

| Item | Value |
|---|---|
| HTTP status | `200` |
| Content-Type | `application/json; charset=utf-8` |
| Response size | 38 bytes |
| JSON top-level keys | `code`, `msg` |
| Nested `data` keys | none |
| `code` | `-1` (non-success) |
| `msg` | `订单号不存在` (order does not exist) |
| Control with a deliberately wrong key (not the configured key) | HTTP `200`; keys `code`, `msg`; `code=-3`; `msg=商户密钥错误` (merchant key error) |

## Verdict

**PASS — reachable and credentials parsed.** The gateway returned HTTP 200 with a well-formed JSON envelope whose keys are `code` and `msg`. The `code=-1` + `订单号不存在` response is the normal "no such order" branch, not a signature error: the daemon's `QueryOrder` maps `code != 1` to `ErrOrderNotFound`. A control query with a deliberately wrong key returned `code=-3` / `商户密钥错误` (merchant key error), confirming the gateway does validate the merchant key and that the configured key is accepted. This proves the gateway is reachable from HMC, TLS works, and the configured merchant credentials were accepted for a read-only order lookup. No order was created and `submit.php` was not called.

## What a real checkout would need

- **Signed checkout call:** a real checkout uses `submit.php` with `pid`, `type`, `out_trade_no`, `money`, `name`, `notify_url`, `return_url`, `sign_type=MD5` and `sign=md5(canonical-sorted-query + KEY)`. This probe deliberately did not call it.
- **Notify URL public reachability:** the gateway must be able to POST the signed payment result from its own network to a public HTTPS URL — normally `{AJS_TENANT_ORIGIN}/v1/payments/epay/notify`, or an explicit `AJS_EPAY_NOTIFY_URL`. A loopback/LAN-only URL or a URL behind authentication will fail silently from the gateway side.
- **Current config gap:** `AJS_TENANT_PORTAL` is unset (the `/v1/portal/*` routes are not registered), and `AJS_TENANT_ORIGIN`, `AJS_EPAY_NOTIFY_URL` and `AJS_EPAY_RETURN_URL` are all unset in the daemon `.env`. A real checkout needs `AJS_TENANT_PORTAL=1` plus an exact `AJS_TENANT_ORIGIN` (or explicit notify/return URLs); without them checkout is unavailable (portal routes 404, or `POST /v1/portal/orders/{id}/checkout` returns 503 `epay checkout is not configured`).
- **Current network gap:** HMC is `192.168.11.4` on a private LAN (WireGuard `10.99.0.2`). Its daemon listens on `*:2261`, but there is no public ingress: the public VPS `152.175.35.40` has no listener or port-forward for 2261, and a direct `10.99.0.2:2261` probe from that VPS timed out. A real checkout needs a public HTTPS reverse proxy or tunnel to HMC's daemon, with the firewall allowing the path; the return URL `{origin}/portal` must be browser-reachable too.
- **Runtime note:** the currently running `ajs-accountd-latest` process (PID 4025859) does not carry `AJS_EPAY_*` in its environment, and a `strings` check of that binary found no `epay_enabled` marker. The epay-capable build must be deployed and restarted with the `.env` loaded before checkout can work.

## Safety

- Merchant key never printed; request URL query never printed.
- No order created; no `submit.php`; no state change.
