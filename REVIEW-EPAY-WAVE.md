# REVIEW — Epay wave (AJS-336)

## Denominators

- **Tree:** `D:/Project/LovePlusSpeedToProxy`, branch `panel-design`, HEAD `07ab82e`; clean worktree used for all commands.
- **Scope:** `daemon/internal/payment/epay.go` (431 lines), `epay_query.go` (388), `epay_test.go` (417), `epay_query_test.go` (255), `daemon/internal/api/portal_orders.go` + `epay_test.go` (363), the settlement path in `daemon/internal/store/orders.go` (416-700), and `DESIGN-ORDERS.md`.
- **Baseline:** `go vet ./...` exit 0; `go test -count=1 ./...` all packages ok; `go test -race -count=1 ./internal/payment ./internal/api` ok (1.1s / 39.4s).
- **Protocol basis:** 易支付 V1 legacy docs (`miaoquka.cn/doc/v1_legacy_api.html`) and KukoPay epay docs: notify is **GET query parameters**; order query returns `status` integer (`1` paid, `0` unpaid, `2` refunded), not `trade_status`.
- **Evidence:** 10 overlay tests in `research/85-epay-audit/evidence/`; 9 intentionally fail against current code, 1 passes (key-in-URL observation). `EPAY-PROBE.md` is a read-only live probe of the target gateway (`dash.natriumgroup.com`); it confirms the query error codes only, not the notify method.
- **Findings:** 6 ranked (4 P1 blockers, 1 P1/P2, 1 P2). **Verdict: NO-GO.**

## Findings

1. **P1 — notify is POST-only and reads only `r.PostForm`; standard 易支付 notify is GET.** `daemon/internal/api/server.go:472`; `daemon/internal/api/portal_orders.go:245-260`. A signed GET gets `405 METHOD_NOT_ALLOWED`; a POST with signed query parameters gets `200 "fail"` because `r.PostForm` is empty. **Falsifier:** send a correctly signed GET to `/v1/payments/epay/notify`; if it returns `success` and settles the order, this finding is false. Current result: 405, order remains `awaiting_payment`.

2. **P1 — order query ignores the standard `status` integer.** `daemon/internal/payment/epay_query.go:40-51,118-137,152-180`. `EpayOrderStatus` has no `Status`; `Paid()` only accepts `trade_status=TRADE_SUCCESS`, which the query API does not return. A standard `{"code":1,...,"status":1}` response is read as unpaid and silently skipped. **Falsifier:** serve `{"code":1,"out_trade_no":"ord-001","money":"30.00","status":1}`; if `Paid()` is true, this finding is false. Current result: `Paid()=false`. The same bounded pass also re-queries the oldest 50 orders forever (`epay_query.go:305-312`), so a 51st paid order is starved; run 51 pending orders for two passes and see whether ord-051 is queried (current result: 0).

3. **P1 — every non-`1` query code becomes `ErrOrderNotFound`; auth errors are silently skipped.** `daemon/internal/payment/epay_query.go:106-108,322-323`. `EPAY-PROBE.md` shows the target gateway returns `code=-1` / `订单号不存在` for a missing order and `code=-3` / `商户密钥错误` for a wrong key. Both become `ErrOrderNotFound`; `ReconcileOnce` `continue`s without audit or log, so a key rotation silently stops all poll-only settlement. **Falsifier:** return `code=-3`; if `QueryOrder` yields `ErrQueryFailed` and a warning, this finding is false. Current result: `ErrOrderNotFound=true`.

4. **P1 — plaintext `http://` gateways are accepted; the query is unsigned and sends the merchant key in the URL.** `daemon/internal/payment/epay.go:105-110`; `daemon/internal/payment/epay_query.go:73-80`. With `http://pay.example.test`, `Enabled()` is true; `QueryOrder` sends `key=<merchant key>` in the URL and trusts an unsigned JSON response. An on-path attacker can steal the key or return forged `status=1`; `http.Client` also follows redirects by default. **Falsifier:** configure a non-loopback `http://` gateway; if the adapter is disabled, this finding is false. Current result: enabled; query URL contains `key=test-secret-key`.

5. **P1/P2 — no currency binding between the plan snapshot and the gateway.** `daemon/internal/payment/epay.go:233-267,331-339`; `daemon/internal/payment/epay_query.go:350-358`; `daemon/internal/store/plans.go:204-218`. Epay `money` has no currency; any three-letter plan currency is accepted, and events stamp `order.Plan.Currency`. A USD plan produces `money=30.00` and a CNY payment is recorded as USD. **Falsifier:** create a USD plan and request checkout; if it is rejected, this finding is false. Current result: HTTP 200, `money="30.00"` for `currency="USD"`.

6. **P2 — a valid epay notify can settle an order that never selected epay.** `daemon/internal/api/portal_orders.go:262-269`; `daemon/internal/payment/epay.go:283-340`; `daemon/internal/store/orders.go:519-527`. New orders default to `Provider: manual`; neither handler nor `VerifyNotify` compares `order.Provider` with the event provider, so a signed notify for a never-checked-out order settles it and flips the provider. **Falsifier:** create an order, skip checkout, POST a valid signed notify; if it stays `awaiting_payment`, this finding is false. Current result: `active`, provider `epay`.

## Self-audit

- **Confirmed sound:** canonical string skips `sign`/`sign_type` and empty values, sorts ASCII keys, joins raw `key=value`, and matches the test vector `2bc86124599cbc9a232826bb4da766ea` independently; duplicate form keys are rejected; signature/PID compares are constant-time; `ParseMoneyMinor` rejects signs/exponents/overflow; the store path is one locked transaction, idempotent by `(provider,event ID)`, duplicate `trade_no` cannot double-grant, amount/currency mismatches and late settlements go to `ReviewRequired`, refund/chargeback revoke the grant, and secrets do not appear in logs/state.
- **Limits:** no live checkout or merchant credential was used; `EPAY-PROBE.md` is a read-only target probe; the current primary tree had unrelated device-slot edits, so all tests ran in a clean worktree. Existing tests cover the adapter's invented `POST`/`trade_status` variant, not the standard protocol (`epay_query_test.go:90,103,141`; `api/epay_test.go:45-53`).
- **SSRF/redirect:** gateway URLs are operator configuration, not user input; the only redirect risk is the default `http.Client` following a gateway redirect, covered in finding 4.
- **Contract:** both OpenAPI copies and `daemon/README.md` document POST form + `trade_status`; they must be corrected with the fixes above.

## Summary

**NO-GO for a real minimum-amount (1-cent) checkout.** A standard gateway's GET notify is rejected (405 or `fail`), and its `status=1` query is read as unpaid; even if one path worked, `code=-3` auth failures are silent and non-CNY plans can mis-settle. Do not run a real checkout until P1-1 through P1-5 are fixed with signed-GET/status=1/wrong-key/currency tests and a sandbox transaction; **GO** only when a signed GET settles an order, `status=1` settles an order, `code=-3` produces an explicit query error, and a 1-cent CNY checkout reaches `active` exactly once.
