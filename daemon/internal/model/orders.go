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

// Payment provider names. "manual" is the offline attestation path; "stripe"
// names the Stripe-compatible webhook adapter and "epay" the 易支付 redirect +
// notify adapter. Online providers stay disabled until their configuration is
// complete.
const (
	ProviderManual = "manual"
	ProviderStripe = "stripe"
	ProviderEpay   = "epay"
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
	// Fulfilment is the durable publication work for this order. Nil means
	// nothing is pending: either no payment is settled yet, the job already
	// committed, or the order is terminal. Exactly one job may exist per
	// order.
	Fulfilment *FulfilmentJob `json:"fulfilment,omitempty"`
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
	// LeaseEndedReason and LeaseEndedAt are read-only projections of the
	// durable leaseEnds row for LeaseID. They are set by the API when the
	// grant's lease is no longer active but its paid term is unexpired, so a
	// customer can see why service stopped and that it can be restored. They
	// are never written back to the order or persisted in a snapshot.
	LeaseEndedReason string `json:"leaseEndedReason,omitempty"`
	LeaseEndedAt     string `json:"leaseEndedAt,omitempty"`
}

// FulfilmentMode is the closed vocabulary of publication work. An initial job
// publishes the first serving lease of a paid order; a reissue job replaces an
// ended lease on an active order without creating a second entitlement.
type FulfilmentMode string

const (
	FulfilmentInitial FulfilmentMode = "initial"
	FulfilmentReissue FulfilmentMode = "reissue"
)

// ValidFulfilmentMode reports whether mode is part of the closed vocabulary.
func ValidFulfilmentMode(mode FulfilmentMode) bool {
	switch mode {
	case FulfilmentInitial, FulfilmentReissue:
		return true
	default:
		return false
	}
}

// FulfilmentState is the closed job vocabulary. A completed job is removed
// from its order, so there is deliberately no "done" state.
type FulfilmentState string

const (
	FulfilmentQueued     FulfilmentState = "queued"
	FulfilmentPublishing FulfilmentState = "publishing"
	FulfilmentDeadLetter FulfilmentState = "dead_letter"
)

// ValidFulfilmentState reports whether state is part of the closed vocabulary.
func ValidFulfilmentState(state FulfilmentState) bool {
	switch state {
	case FulfilmentQueued, FulfilmentPublishing, FulfilmentDeadLetter:
		return true
	default:
		return false
	}
}

// FulfilmentReason is the closed vocabulary of stable refusal codes a job may
// record. It is deliberately not free text: a payment reference, signed URL or
// credential must never reach the durable job or its audit rows.
type FulfilmentReason string

const (
	// FulfilmentReasonCapacity means no account/node had a free slot.
	FulfilmentReasonCapacity FulfilmentReason = "capacity"
	// FulfilmentReasonTenantSuspended means the owning tenant is not active.
	FulfilmentReasonTenantSuspended FulfilmentReason = "tenant_suspended"
	// FulfilmentReasonTargetChanged means the selected node/slot/generation
	// no longer matches what the attempt reserved.
	FulfilmentReasonTargetChanged FulfilmentReason = "target_changed"
	// FulfilmentReasonPublishFailed means the external publication failed.
	FulfilmentReasonPublishFailed FulfilmentReason = "publish_failed"
	// FulfilmentReasonTimeout means publication did not answer in time.
	FulfilmentReasonTimeout FulfilmentReason = "timeout"
	// FulfilmentReasonInternal means the attempt failed for an unclassified
	// internal reason.
	FulfilmentReasonInternal FulfilmentReason = "internal"
	// FulfilmentReasonInterrupted means a restart requeued the attempt; no
	// attempt actually failed.
	FulfilmentReasonInterrupted FulfilmentReason = "interrupted"
	// FulfilmentReasonEntitlementExpired means the paid term ended before a
	// publication could commit, so the job is dead-lettered without service.
	FulfilmentReasonEntitlementExpired FulfilmentReason = "entitlement_expired"
)

// ValidFulfilmentReason reports whether reason is part of the closed
// vocabulary. The empty value is not a reason.
func ValidFulfilmentReason(reason FulfilmentReason) bool {
	switch reason {
	case FulfilmentReasonCapacity, FulfilmentReasonTenantSuspended,
		FulfilmentReasonTargetChanged, FulfilmentReasonPublishFailed,
		FulfilmentReasonTimeout, FulfilmentReasonInternal,
		FulfilmentReasonInterrupted, FulfilmentReasonEntitlementExpired:
		return true
	default:
		return false
	}
}

// FulfilmentJob is the durable publication work one order owns. The store
// creates it once, keeps it across restarts and removes it only after the
// entitlement commit; a nil Order.Fulfilment is the absence of work.
//
// Attempts counts real publication attempts, not queued waits or duplicate
// callbacks. AttemptLeaseID is the fencing token of the attempt currently
// publishing, so a late abort from an earlier attempt cannot rewrite the
// current schedule. EntitlementExpiresAt freezes the paid term the job must
// not outlive: PaidAt+LeaseTTLSeconds for initial work, the grant's original
// expiry for a reissue. NextAttemptAt is nil while an attempt is in flight and
// for a dead-lettered job. LastReason is the closed refusal code; LastError is
// a bounded, sanitized detail. Blocked marks a dead-lettered job that needs an
// operator; it is false for every job that may still run automatically.
type FulfilmentJob struct {
	OrderID              string           `json:"orderId"`
	Mode                 FulfilmentMode   `json:"mode"`
	State                FulfilmentState  `json:"state"`
	Attempts             int              `json:"attempts"`
	AttemptLeaseID       string           `json:"attemptLeaseId,omitempty"`
	EntitlementExpiresAt string           `json:"entitlementExpiresAt"`
	NextAttemptAt        *string          `json:"nextAttemptAt,omitempty"`
	LastReason           FulfilmentReason `json:"lastReason,omitempty"`
	LastError            string           `json:"lastError,omitempty"`
	Blocked              bool             `json:"blocked,omitempty"`
	// PublishedAt and PublishedLeaseID are the durable proof that the
	// attempt's provisional lease was accepted by the publisher. Once set,
	// the job may only commit that exact lease: a retry must never publish a
	// second identity while the first publication is unaccounted for. The
	// marker is cleared when the job commits or is dead-lettered, and its
	// reservation is released at the same time.
	PublishedAt        *string `json:"publishedAt,omitempty"`
	PublishedLeaseID   string  `json:"publishedLeaseId,omitempty"`
	PublishedAccountID string  `json:"publishedAccountId,omitempty"`
	// CleanupPending marks a published attempt whose external withdrawal
	// failed. The published identity is retained — never released and never
	// dead-lettered as if cleanup succeeded — and the worker retries the
	// withdrawal. The order is flagged for review so no automatic service is
	// claimed while an externally accepted reservation may still exist.
	CleanupPending bool   `json:"cleanupPending,omitempty"`
	UpdatedAt      string `json:"updatedAt"`
}

// LeasePendingPublication is a provisional serving lease reserved by one order
// fulfilment attempt. It holds its node slot but is not serving: only the
// fulfilment commit promotes it to active, while an abort or startup recovery
// removes it. Normal admission counts it as a reservation, and no customer
// path may treat it as a live lease.
const LeasePendingPublication LeaseStatus = "pending_publication"

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
