package payment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// Manual reference bounds mirror the store's: a receipt reference is one
// bounded line, never a secret.
const (
	maxReferenceLen = 200
)

// Manual is the offline settlement provider. It is always enabled because it
// requires an authenticated admin action; it cannot take money and never
// reports that an online transaction occurred.
type Manual struct {
	now func() time.Time
}

// NewManual builds the manual provider. A nil clock uses time.Now.
func NewManual(now func() time.Time) *Manual {
	if now == nil {
		now = time.Now
	}
	return &Manual{now: now}
}

// Name implements PaymentProvider.
func (m *Manual) Name() string { return model.ProviderManual }

// Mode implements PaymentProvider.
func (m *Manual) Mode() string { return "manual" }

// AttestSettlement reduces an admin's offline receipt attestation to one
// settled event. The store stamps the immutable snapshot amount, so the
// provider needs no order lookup. The event ID is derived from the order and
// reference, which makes a retry replay the recorded result.
func (m *Manual) AttestSettlement(ctx context.Context, orderID, reference string) (model.PaymentEvent, error) {
	if err := ctx.Err(); err != nil {
		return model.PaymentEvent{}, err
	}
	orderID = strings.TrimSpace(orderID)
	reference = strings.TrimSpace(reference)
	if orderID == "" {
		return model.PaymentEvent{}, ErrInvalidEvent
	}
	if reference == "" || len(reference) > maxReferenceLen ||
		strings.ContainsAny(reference, "\r\n\t") {
		return model.PaymentEvent{}, ErrInvalidReference
	}
	return model.PaymentEvent{
		Provider:    model.ProviderManual,
		ID:          manualEventID("settled", orderID, reference),
		Kind:        model.PaymentSettled,
		OrderID:     orderID,
		ProviderRef: reference,
		OccurredAt:  m.now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// manualEventID is deliberately deterministic: a retried attestation carries
// the same provider event ID, so the store replays instead of double-granting.
func manualEventID(kind, orderID, reference string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + orderID + "\x00" + reference))
	return "manual-" + hex.EncodeToString(sum[:12])
}
