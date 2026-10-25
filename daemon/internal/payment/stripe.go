package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// Stripe signature defaults. The timestamp tolerance bounds replay of a
// captured body; five minutes is the provider's documented window.
const (
	DefaultStripeTolerance = 5 * time.Minute
	stripeSignatureHeader  = "Stripe-Signature"
)

// StripeConfig configures the Stripe-compatible webhook adapter. An empty
// Secret keeps the adapter disabled: VerifyWebhook returns ErrDisabled and the
// API answers 503, never falling back to manual settlement.
type StripeConfig struct {
	Secret    string
	Tolerance time.Duration
	Now       func() time.Time
}

// StripeCompatible verifies Stripe-Signature over the raw body and reduces an
// allowlisted event to one model.PaymentEvent. It stores no signature, secret
// or raw payload.
type StripeCompatible struct {
	secret    string
	tolerance time.Duration
	now       func() time.Time
}

// NewStripeCompatible builds the adapter. A nil clock uses time.Now; a
// non-positive tolerance uses DefaultStripeTolerance.
func NewStripeCompatible(cfg StripeConfig) *StripeCompatible {
	tolerance := cfg.Tolerance
	if tolerance <= 0 {
		tolerance = DefaultStripeTolerance
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &StripeCompatible{secret: cfg.Secret, tolerance: tolerance, now: now}
}

// Name implements PaymentProvider.
func (p *StripeCompatible) Name() string { return model.ProviderStripe }

// Mode implements PaymentProvider.
func (p *StripeCompatible) Mode() string { return "webhook" }

// Enabled reports whether a webhook secret is configured.
func (p *StripeCompatible) Enabled() bool { return strings.TrimSpace(p.secret) != "" }

// VerifyWebhook implements WebhookProvider. The signature is checked against
// the exact bytes received, before any JSON parsing, with a constant-time
// comparison and a timestamp tolerance. Only checkout completion, refund and
// dispute events are mapped; every other type is rejected without mutation.
func (p *StripeCompatible) VerifyWebhook(ctx context.Context, body []byte, header http.Header) (model.PaymentEvent, error) {
	if err := ctx.Err(); err != nil {
		return model.PaymentEvent{}, err
	}
	if !p.Enabled() {
		return model.PaymentEvent{}, ErrDisabled
	}
	timestamp, signatures, err := parseStripeSignature(header.Get(stripeSignatureHeader))
	if err != nil {
		return model.PaymentEvent{}, err
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return model.PaymentEvent{}, ErrInvalidSignature
	}
	now := p.now()
	skew := now.Unix() - seconds
	if skew < 0 {
		skew = -skew
	}
	if skew > int64(p.tolerance/time.Second) {
		return model.PaymentEvent{}, ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, []byte(p.secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := mac.Sum(nil)
	matched := 0
	for _, signature := range signatures {
		matched |= subtle.ConstantTimeCompare(expected, signature)
	}
	if matched != 1 {
		return model.PaymentEvent{}, ErrInvalidSignature
	}

	var payload stripeEvent
	if err := json.Unmarshal(body, &payload); err != nil {
		return model.PaymentEvent{}, ErrInvalidEvent
	}
	payload.ID = strings.TrimSpace(payload.ID)
	if payload.ID == "" {
		return model.PaymentEvent{}, ErrInvalidEvent
	}
	object := payload.Data.Object
	var kind model.PaymentEventKind
	switch payload.Type {
	case "checkout.session.completed":
		// A delayed payment method completes the session before the money
		// arrives (payment_status "unpaid"); only async_payment_succeeded or a
		// paid session is a settlement fact.
		if object.PaymentStatus != "" && object.PaymentStatus != "paid" {
			return model.PaymentEvent{}, ErrUnsupportedEvent
		}
		kind = model.PaymentSettled
	case "checkout.session.async_payment_succeeded":
		kind = model.PaymentSettled
	case "charge.refunded":
		kind = model.PaymentRefunded
	case "charge.dispute.created":
		kind = model.PaymentChargedBack
	default:
		return model.PaymentEvent{}, ErrUnsupportedEvent
	}
	orderID := metadataOrderID(object)
	providerRef := strings.TrimSpace(object.PaymentIntent)
	if providerRef == "" {
		providerRef = strings.TrimSpace(object.ID)
	}
	currency, ok := stripeCurrency(object.Currency)
	if !ok || orderID == "" || providerRef == "" {
		return model.PaymentEvent{}, ErrInvalidEvent
	}

	event := model.PaymentEvent{
		Provider:    model.ProviderStripe,
		ID:          payload.ID,
		Kind:        kind,
		OrderID:     orderID,
		ProviderRef: providerRef,
		Currency:    currency,
		OccurredAt:  stripeOccurredAt(payload.Created, now),
	}
	switch kind {
	case model.PaymentSettled:
		event.AmountMinor = object.AmountTotal
	case model.PaymentRefunded:
		event.AmountMinor = object.AmountRefunded
	case model.PaymentChargedBack:
		event.AmountMinor = object.Amount
	}
	if event.AmountMinor <= 0 {
		return model.PaymentEvent{}, ErrInvalidEvent
	}
	return event, nil
}

// parseStripeSignature decodes the `t=...,v1=...` header. Multiple v1 entries
// are accepted because a provider may rotate its signing key.
func parseStripeSignature(header string) (string, [][]byte, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", nil, ErrInvalidSignature
	}
	timestamp := ""
	var signatures [][]byte
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "t":
			if timestamp != "" {
				return "", nil, ErrInvalidSignature
			}
			timestamp = value
		case "v1":
			decoded, err := hex.DecodeString(value)
			if err != nil || len(decoded) != sha256.Size {
				continue
			}
			signatures = append(signatures, decoded)
		}
	}
	if timestamp == "" || len(signatures) == 0 {
		return "", nil, ErrInvalidSignature
	}
	return timestamp, signatures, nil
}

func stripeCurrency(raw string) (string, bool) {
	currency := strings.ToUpper(strings.TrimSpace(raw))
	if len(currency) != 3 {
		return "", false
	}
	for i := range len(currency) {
		if currency[i] < 'A' || currency[i] > 'Z' {
			return "", false
		}
	}
	return currency, true
}

func metadataOrderID(object stripeObject) string {
	for _, key := range []string{"orderId", "order_id"} {
		if value := strings.TrimSpace(object.Metadata[key]); value != "" {
			return value
		}
	}
	return ""
}

func stripeOccurredAt(created int64, now time.Time) string {
	if created <= 0 {
		return now.UTC().Format(time.RFC3339Nano)
	}
	return time.Unix(created, 0).UTC().Format(time.RFC3339Nano)
}

// stripeEvent is the bounded projection of a Stripe callback. Unknown fields
// are ignored by encoding/json and never persisted.
type stripeEvent struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Created int64  `json:"created"`
	Data    struct {
		Object stripeObject `json:"object"`
	} `json:"data"`
}

type stripeObject struct {
	ID             string            `json:"id"`
	Amount         int64             `json:"amount"`
	AmountTotal    int64             `json:"amount_total"`
	AmountRefunded int64             `json:"amount_refunded"`
	Currency       string            `json:"currency"`
	PaymentIntent  string            `json:"payment_intent"`
	PaymentStatus  string            `json:"payment_status"`
	Metadata       map[string]string `json:"metadata"`
}
