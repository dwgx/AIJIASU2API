# EPAY / payments status

Status: **experimental, disabled in `v0.1.0-pre.3`** (published 2026-10-29). This page is the
single answer to "can I pay, or be paid, through this software?" The
answer is **no**.

## What this is not

- It is **not a payment service**, payment processor, merchant of record or escrow.
- It does **not** charge a card, a wallet or a bank account.
- It does **not** hold, transfer or settle funds.
- It is **not** a shipping feature of `v0.1.0-pre.3`.

The code is AI-generated and reviewed like the rest of the tree. Treat it as an experimental
integration surface, not as audited financial software.

## What exists in the tree

Two provider adapters and an order/entitlement model exist behind the customer-portal switch:

| Piece | Switch | Behaviour |
|---|---|---|
| Customer portal, orders, tenant admin, plans | `AJS_TENANT_PORTAL=1` + `AJS_TENANT_ORIGIN` | registers `/v1/portal/*`, `/v1/admin/tenants*`, `/v1/admin/orders*`, `/v1/admin/plans*`; with the switch off every one of them answers `404` |
| 易支付 (epay) hosted checkout | `AJS_EPAY_GATEWAY`, `AJS_EPAY_PID`, `AJS_EPAY_KEY`, `AJS_EPAY_SIGN_TYPE=MD5` | creates a hosted `payUrl`; verifies the signed form notify; reconciles by a signed query about every 60 s |
| Stripe-compatible callback | `AJS_STRIPE_WEBHOOK_SECRET` | verifies the provider signature; unset leaves `POST /v1/payments/stripe` at `503` |
| Manual settlement | admin command | records an attestation that funds arrived elsewhere; activates the entitlement |

Safety properties that are implemented and tested offline:

- A created order is never a payment success; confirmation and activation are separate audited
  commands.
- The epay notify verifies `trade_status` and the signature; the query reconciler accepts the
  standard `status=1` or `TRADE_SUCCESS`; auth failures map to `ErrQueryAuth`; provider
  mismatch is rejected; a plaintext gateway is refused except on loopback.
- Refund and chargeback record evidence and revoke the grant; they transfer nothing.
- No route stores card data or sends a payment request.

## State in `v0.1.0-pre.3`

- `AJS_TENANT_PORTAL=0` in the shipped environment example. The portal, order, plan and payment
  routes are **not registered**, so they answer `404`.
- `ajs-portal.conf` is marked non-product and is not installed by the release procedure.
- No real epay or Stripe event has reached the daemon outside `httptest`. No customer has
  created an order. No administrator has attested a real receipt.
- On the live host the epay environment variables are set but `AJS_TENANT_PORTAL` is absent, so
  the adapter is **configured but unreachable**: there is no order surface to call it from.
- There is no pricing information in the docs by design.

## History

An earlier review of this wave (`REVIEW-EPAY-WAVE.md`) recorded **NO-GO**. The specific code
findings from that review — GET query parsing, the standard `status` integer, auth-error
mapping, plaintext-gateway refusal and provider mismatch — were subsequently addressed and are
covered by tests. The verdict itself was not a release approval, and this release does not
treat it as one. The feature remains experimental.

## Re-enabling

Re-enabling payments requires all of the following, in order:

1. An explicit product decision to ship a paid surface, recorded outside this file.
2. Restoring and reviewing the customer UI (the portal routes and bundle) that the current
   release deliberately leaves off.
3. A fresh security review of the order, settlement, refund and webhook paths against a real
   provider sandbox, including replay, signature-failure and amount-mismatch cases.
4. Legal, tax and refund-policy review for the jurisdictions served.
5. Provider credentials supplied by the operator, never committed; a documented rotation path.
6. A live drill with a real (sandbox or minimum-value) transaction, recorded with identifiers
   only.
7. Updating [`docs/PRIVACY.md`](PRIVACY.md), [`docs/OPERATIONS.md`](OPERATIONS.md) and the
   release notes, and removing this page's "disabled" status.

Until then, keep `AJS_TENANT_PORTAL=0`, do not install the portal vhost, and do not describe
the software as accepting payments.
