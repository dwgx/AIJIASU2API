// Package payment defines the settlement provider boundary. A provider turns
// an external fact (an admin's offline receipt attestation or a verified
// webhook) into one model.PaymentEvent; it never mutates an order itself.
// The store applies events through a single locked transaction.
package payment

import (
	"context"
	"errors"
	"net/http"

	"ajsaccountd/internal/model"
)

// Provider failures. The API maps these onto its error envelope: a disabled
// provider is 503, a rejected signature or event is 400, and a reference error
// is 400.
var (
	ErrDisabled         = errors.New("payment provider is disabled")
	ErrInvalidSignature = errors.New("invalid webhook signature")
	ErrInvalidEvent     = errors.New("invalid payment event")
	ErrInvalidReference = errors.New("invalid settlement reference")
	ErrUnsupportedEvent = errors.New("unsupported payment event")
)

// PaymentProvider is the identity every adapter exposes. Mode is "manual" for
// an admin attestation and "webhook" for a signed provider callback.
type PaymentProvider interface {
	Name() string
	Mode() string
}

// ManualSettlementProvider records an offline payment an admin has verified.
// It must never claim an online transaction occurred.
type ManualSettlementProvider interface {
	PaymentProvider
	AttestSettlement(ctx context.Context, orderID, reference string) (model.PaymentEvent, error)
}

// WebhookProvider verifies and reduces a provider callback. Implementations
// must verify the raw body before parsing it and must reject a disabled state
// rather than falling back to manual settlement.
type WebhookProvider interface {
	PaymentProvider
	VerifyWebhook(ctx context.Context, body []byte, h http.Header) (model.PaymentEvent, error)
}
