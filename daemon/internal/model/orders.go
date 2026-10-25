package model

// Plan is one admin-configured order catalogue entry. It is configuration, not
// a contract with any customer: an order copies the priced fields into an
// immutable snapshot at creation time, so a later edit or deactivation only
// changes which plans new orders may use (DESIGN-ORDERS §Decision).
type Plan struct {
	ID              string `json:"id"`
	Active          bool   `json:"active"`
	Name            string `json:"name"`
	AmountMinor     int64  `json:"amountMinor"`
	Currency        string `json:"currency"`
	LeaseTTLSeconds int    `json:"leaseTtlSeconds"`
	QuotaBytes      int64  `json:"quotaBytes"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

// PlanSnapshot is the immutable priced copy an order carries. It deliberately
// has no Active, CreatedAt or UpdatedAt: activation must never consult the
// mutable plan row, and an order stays readable after the plan is edited.
type PlanSnapshot struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	AmountMinor     int64  `json:"amountMinor"`
	Currency        string `json:"currency"`
	LeaseTTLSeconds int    `json:"leaseTtlSeconds"`
	QuotaBytes      int64  `json:"quotaBytes"`
}

// PlanInput is the POST /v1/admin/plans body. Active defaults to true when
// omitted; an explicit false creates a disabled draft.
type PlanInput struct {
	Name            string `json:"name"`
	AmountMinor     int64  `json:"amountMinor"`
	Currency        string `json:"currency"`
	LeaseTTLSeconds int    `json:"leaseTtlSeconds"`
	QuotaBytes      int64  `json:"quotaBytes"`
	Active          *bool  `json:"active"`
}

// PlanPatch is the PATCH /v1/admin/plans/{id} body. Every field is optional;
// an explicit false deactivates and true reactivates.
type PlanPatch struct {
	Name            *string `json:"name"`
	AmountMinor     *int64  `json:"amountMinor"`
	Currency        *string `json:"currency"`
	LeaseTTLSeconds *int    `json:"leaseTtlSeconds"`
	QuotaBytes      *int64  `json:"quotaBytes"`
	Active          *bool   `json:"active"`
}

// OrderState is the closed order lifecycle vocabulary. Terminal states never
// resume: a late payment or reversal is recorded for review instead.
type OrderState string

const (
	OrderAwaitingPayment OrderState = "awaiting_payment"
	OrderPaid            OrderState = "paid"
	OrderActive          OrderState = "active"
	OrderCancelled       OrderState = "cancelled"
	OrderRefunded        OrderState = "refunded"
	OrderChargedBack     OrderState = "charged_back"
)

// ValidOrderState reports whether state is part of the closed vocabulary.
func ValidOrderState(state OrderState) bool {
	switch state {
	case OrderAwaitingPayment, OrderPaid, OrderActive,
		OrderCancelled, OrderRefunded, OrderChargedBack:
		return true
	default:
		return false
	}
}

// Payment provider names. "manual" is the only live provider; "stripe" names
// the Stripe-compatible webhook adapter and stays disabled until a secret is
// configured.
const (
	ProviderManual = "manual"
	ProviderStripe = "stripe"
)

// Order is the customer-visible order. The plan snapshot is immutable; the
// idempotency and request hashes are store-only and never part of this type.
type Order struct {
	ID             string            `json:"id"`
	TenantID       string            `json:"tenantId"`
	UserID         string            `json:"userId"`
	PlanID         string            `json:"planId"`
	Plan           PlanSnapshot      `json:"plan"`
	Provider       string            `json:"provider"`
	State          OrderState        `json:"state"`
	ProviderRef    *string           `json:"providerRef"`
	ReviewRequired bool              `json:"reviewRequired"`
	CreatedAt      string            `json:"createdAt"`
	PaidAt         *string           `json:"paidAt"`
	ActivatedAt    *string           `json:"activatedAt"`
	CancelledAt    *string           `json:"cancelledAt"`
	RefundedAt     *string           `json:"refundedAt"`
	Grant          *EntitlementGrant `json:"grant"`
}

// EntitlementGrant is the single entitlement one order may own. OrderID is
// unique in the store, which is what makes activation replay-safe. RevokedAt
// is set once; PreviousQuotaBytes is restored on revocation.
type EntitlementGrant struct {
	OrderID            string  `json:"orderId"`
	LeaseID            string  `json:"leaseId"`
	QuotaBytes         int64   `json:"quotaBytes"`
	PreviousQuotaBytes *int64  `json:"previousQuotaBytes"`
	ExpiresAt          string  `json:"expiresAt"`
	RevokedAt          *string `json:"revokedAt"`
}

// OrderCreateRequest is the POST /v1/portal/orders body. The idempotency key
// travels in the Idempotency-Key header, never in the body.
type OrderCreateRequest struct {
	PlanID string `json:"planId"`
}

// OrderSettleRequest is the POST /v1/admin/orders/{id}/settle body. The
// attestation must be explicit: an admin confirms offline funds were received
// and supplies the receipt reference. Neither field implies an online payment.
type OrderSettleRequest struct {
	Reference            string `json:"reference"`
	OfflineFundsReceived bool   `json:"offlineFundsReceived"`
}

// OrderRefundRequest is the POST /v1/admin/orders/{id}/refund body. It records
// an already completed offline refund; it never transfers money.
type OrderRefundRequest struct {
	Reference string `json:"reference"`
	Reason    string `json:"reason"`
}

// OrderChargebackRequest is the POST /v1/admin/orders/{id}/chargeback body.
// It records external evidence of a dispute.
type OrderChargebackRequest struct {
	Reference string `json:"reference"`
	Reason    string `json:"reason"`
}

// PaymentEventKind is the closed provider-event vocabulary.
type PaymentEventKind string

const (
	PaymentSettled     PaymentEventKind = "settled"
	PaymentRefunded    PaymentEventKind = "refunded"
	PaymentChargedBack PaymentEventKind = "charged_back"
)

// ValidPaymentEventKind reports whether kind is part of the closed vocabulary.
func ValidPaymentEventKind(kind PaymentEventKind) bool {
	switch kind {
	case PaymentSettled, PaymentRefunded, PaymentChargedBack:
		return true
	default:
		return false
	}
}

// PaymentEventResult is what the store did with one provider event. The row is
// retained even when it changed nothing, so a duplicate ID replays the recorded
// result instead of being applied twice.
type PaymentEventResult string

const (
	PaymentApplied    PaymentEventResult = "applied"
	PaymentDuplicate  PaymentEventResult = "duplicate"
	PaymentReviewOnly PaymentEventResult = "review_required"
	PaymentNoOp       PaymentEventResult = "no_op"
)

// PaymentEvent is one provider-reported fact. It carries no card data, raw
// payload or signature: the provider adapter reduces its input to these fields
// before the store sees it.
type PaymentEvent struct {
	Provider    string             `json:"provider"`
	ID          string             `json:"id"`
	Kind        PaymentEventKind   `json:"kind"`
	OrderID     string             `json:"orderId"`
	ProviderRef string             `json:"providerRef"`
	AmountMinor int64              `json:"amountMinor"`
	Currency    string             `json:"currency"`
	OccurredAt  string             `json:"occurredAt"`
	Result      PaymentEventResult `json:"result,omitempty"`
}
