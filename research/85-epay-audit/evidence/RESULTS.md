# Epay audit — evidence results

All commands were run from a detached clean worktree at `D:/d/tmp/epay-audit-head` (HEAD `07ab82e`), because the primary working tree contained unrelated uncommitted device-slot changes during the review.

## Baseline

```
go version go1.26.7 windows/amd64
go vet ./...                         -> exit 0
go test -count=1 ./...               -> all 21 packages ok
```

## Overlay tests (intentionally fail against the current code)

```
cd daemon
go test -count=1 -overlay=D:/Project/LovePlusSpeedToProxy/research/85-epay-audit/evidence/overlay-epay-worktree.json ./internal/payment -run 'TestOverlayEpay' -v
go test -count=1 -overlay=D:/Project/LovePlusSpeedToProxy/research/85-epay-audit/evidence/overlay-epay-worktree.json ./internal/api -run 'TestOverlayEpay' -v
```

Payment package:

```
--- FAIL: TestOverlayEpayQueryStandardStatusField
    standard status=1 response -> Paid()=false TradeStatus=""
    standard 易支付 order-query status=1 was not recognized as paid
--- FAIL: TestOverlayEpayQueryErrorCodeIsNotOrderNotFound
    gateway code=-1 -> err=epay order not found ErrOrderNotFound=true
    a gateway authentication/configuration error is classified as order-not-found
--- FAIL: TestOverlayEpayPlaintextGatewayAccepted
    http gateway: Enabled=true
    plaintext http gateway is accepted; the query path sends the merchant key in the URL
--- FAIL: TestOverlayEpayReconcileStarvesNewestOrders
    queried 50 distinct orders over two passes; ord-051 queried 0 times
    the 51st pending order was never queried: the oldest 50 are re-queried every pass
--- PASS: TestOverlayEpayQueryKeyInURL
    query URL: /api.php?act=order&key=test-secret-key&out_trade_no=ord-001&pid=1001
```

API package:

```
--- FAIL: TestOverlayEpayNotifyGET
    GET notify -> HTTP 405 ... METHOD_NOT_ALLOWED
    order after GET notify: state=awaiting_payment provider=epay
--- FAIL: TestOverlayEpayNotifyPOSTQueryParams
    POST notify with query params -> HTTP 200 body="fail"
    order after POST query notify: state=awaiting_payment
--- FAIL: TestOverlayEpayNotifySettlesNonEpayOrder
    POST notify for never-checked-out order -> HTTP 200 body="success"
    order after notify: state=active provider=epay
--- FAIL: TestOverlayEpayCheckoutAcceptsNonCNYPlan
    USD plan checkout -> HTTP 200
    payUrl money="30.00" for plan currency="USD"
--- FAIL: TestOverlayEpaySettingsStatus
    GET /v1/settings keys=[transport healthIntervalSec leaseTtl subscriptionRefreshSec theme] epayEnabled=false
```

The failures are the intended reproduction of the P1/P2 findings in `REVIEW-EPAY-WAVE.md`; they are not regressions in the clean baseline.
