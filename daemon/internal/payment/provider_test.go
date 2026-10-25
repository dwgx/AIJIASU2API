package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

func TestManualAttestationRequiresReferenceAndIsDeterministic(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	provider := NewManual(func() time.Time { return now })
	if provider.Name() != model.ProviderManual || provider.Mode() != "manual" {
		t.Fatalf("manual provider identity = %s/%s", provider.Name(), provider.Mode())
	}
	for _, reference := range []string{"", "   ", "line\nbreak", strings.Repeat("x", 201)} {
		if _, err := provider.AttestSettlement(context.Background(), "ord-001", reference); !errors.Is(err, ErrInvalidReference) {
			t.Fatalf("reference %q error = %v, want ErrInvalidReference", reference, err)
		}
	}
	if _, err := provider.AttestSettlement(context.Background(), "", "receipt"); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("empty order error = %v, want ErrInvalidEvent", err)
	}

	first, err := provider.AttestSettlement(context.Background(), "ord-001", " receipt-1 ")
	if err != nil {
		t.Fatalf("AttestSettlement: %v", err)
	}
	if first.Kind != model.PaymentSettled || first.Provider != model.ProviderManual ||
		first.ProviderRef != "receipt-1" || first.AmountMinor != 0 || first.Currency != "" {
		t.Fatalf("manual event = %+v", first)
	}
	if first.OccurredAt != now.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("occurredAt = %q", first.OccurredAt)
	}
	second, _ := provider.AttestSettlement(context.Background(), "ord-001", "receipt-1")
	if second.ID != first.ID {
		t.Fatalf("retried attestation changed the event id: %s vs %s", second.ID, first.ID)
	}
	other, _ := provider.AttestSettlement(context.Background(), "ord-001", "receipt-2")
	if other.ID == first.ID {
		t.Fatalf("different references share an event id")
	}
}

func stripeSignature(secret string, body []byte, timestamp int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", timestamp, hex.EncodeToString(mac.Sum(nil)))
}

func stripeHeader(secret string, body []byte, timestamp int64) http.Header {
	header := http.Header{}
	header.Set(stripeSignatureHeader, stripeSignature(secret, body, timestamp))
	return header
}

const stripeTestSecret = "whsec_test_secret"

func TestStripeDisabledWithoutSecret(t *testing.T) {
	provider := NewStripeCompatible(StripeConfig{})
	if provider.Enabled() {
		t.Fatalf("provider reports enabled without a secret")
	}
	if _, err := provider.VerifyWebhook(context.Background(), []byte("{}"), http.Header{}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled VerifyWebhook error = %v, want ErrDisabled", err)
	}
}

func TestStripeVerifiesAndMapsAllowlistedEvents(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	provider := NewStripeCompatible(StripeConfig{Secret: stripeTestSecret, Now: func() time.Time { return now }})
	if provider.Name() != model.ProviderStripe || provider.Mode() != "webhook" {
		t.Fatalf("stripe provider identity = %s/%s", provider.Name(), provider.Mode())
	}
	ts := now.Unix()

	checkout := []byte(`{"id":"evt_checkout","type":"checkout.session.completed","created":1759319999,"data":{"object":{"id":"cs_1","amount_total":3000,"currency":"cny","payment_intent":"pi_1","metadata":{"orderId":"ord-001"}}}}`)
	event, err := provider.VerifyWebhook(context.Background(), checkout, stripeHeader(stripeTestSecret, checkout, ts))
	if err != nil {
		t.Fatalf("checkout VerifyWebhook: %v", err)
	}
	if event.Provider != model.ProviderStripe || event.ID != "evt_checkout" ||
		event.Kind != model.PaymentSettled || event.OrderID != "ord-001" ||
		event.ProviderRef != "pi_1" || event.AmountMinor != 3000 || event.Currency != "CNY" {
		t.Fatalf("checkout event = %+v", event)
	}
	if event.OccurredAt != time.Unix(1759319999, 0).UTC().Format(time.RFC3339Nano) {
		t.Fatalf("occurredAt = %q", event.OccurredAt)
	}

	refund := []byte(`{"id":"evt_refund","type":"charge.refunded","created":1759320000,"data":{"object":{"id":"ch_1","amount_refunded":1200,"currency":"cny","payment_intent":"pi_1","metadata":{"orderId":"ord-001"}}}}`)
	event, err = provider.VerifyWebhook(context.Background(), refund, stripeHeader(stripeTestSecret, refund, ts))
	if err != nil {
		t.Fatalf("refund VerifyWebhook: %v", err)
	}
	if event.Kind != model.PaymentRefunded || event.AmountMinor != 1200 || event.ProviderRef != "pi_1" {
		t.Fatalf("refund event = %+v", event)
	}

	dispute := []byte(`{"id":"evt_dispute","type":"charge.dispute.created","created":1759320001,"data":{"object":{"id":"dp_1","amount":3000,"currency":"cny","payment_intent":"pi_1","metadata":{"order_id":"ord-001"}}}}`)
	event, err = provider.VerifyWebhook(context.Background(), dispute, stripeHeader(stripeTestSecret, dispute, ts))
	if err != nil {
		t.Fatalf("dispute VerifyWebhook: %v", err)
	}
	if event.Kind != model.PaymentChargedBack || event.OrderID != "ord-001" || event.AmountMinor != 3000 {
		t.Fatalf("dispute event = %+v", event)
	}
}

func TestStripeRejectsBadSignatureStaleTimestampAndUnknownEvent(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	provider := NewStripeCompatible(StripeConfig{Secret: stripeTestSecret, Now: func() time.Time { return now }})
	body := []byte(`{"id":"evt_1","type":"checkout.session.completed","created":1759319999,"data":{"object":{"id":"cs_1","amount_total":3000,"currency":"cny","metadata":{"orderId":"ord-001"}}}}`)

	if _, err := provider.VerifyWebhook(context.Background(), body, http.Header{}); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("missing header error = %v, want ErrInvalidSignature", err)
	}
	bad := http.Header{}
	bad.Set(stripeSignatureHeader, stripeSignature("wrong-secret", body, now.Unix()))
	if _, err := provider.VerifyWebhook(context.Background(), body, bad); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("bad signature error = %v, want ErrInvalidSignature", err)
	}
	stale := stripeHeader(stripeTestSecret, body, now.Add(-10*time.Minute).Unix())
	if _, err := provider.VerifyWebhook(context.Background(), body, stale); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("stale signature error = %v, want ErrInvalidSignature", err)
	}
	// A tampered body fails even with a signature for the original bytes.
	signed := stripeHeader(stripeTestSecret, body, now.Unix())
	tampered := append(append([]byte(nil), body...), ' ')
	if _, err := provider.VerifyWebhook(context.Background(), tampered, signed); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("tampered body error = %v, want ErrInvalidSignature", err)
	}

	unknown := []byte(`{"id":"evt_2","type":"customer.created","created":1759319999,"data":{"object":{"id":"cus_1","currency":"cny","metadata":{"orderId":"ord-001"}}}}`)
	if _, err := provider.VerifyWebhook(context.Background(), unknown, stripeHeader(stripeTestSecret, unknown, now.Unix())); !errors.Is(err, ErrUnsupportedEvent) {
		t.Fatalf("unknown event error = %v, want ErrUnsupportedEvent", err)
	}

	noOrder := []byte(`{"id":"evt_3","type":"checkout.session.completed","created":1759319999,"data":{"object":{"id":"cs_1","amount_total":3000,"currency":"cny","metadata":{}}}}`)
	if _, err := provider.VerifyWebhook(context.Background(), noOrder, stripeHeader(stripeTestSecret, noOrder, now.Unix())); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("missing order metadata error = %v, want ErrInvalidEvent", err)
	}

	// A delayed payment method completes the session before the money arrives;
	// that is not a settlement fact.
	unpaid := []byte(`{"id":"evt_4","type":"checkout.session.completed","created":1759319999,"data":{"object":{"id":"cs_1","amount_total":3000,"currency":"cny","payment_status":"unpaid","metadata":{"orderId":"ord-001"}}}}`)
	if _, err := provider.VerifyWebhook(context.Background(), unpaid, stripeHeader(stripeTestSecret, unpaid, now.Unix())); !errors.Is(err, ErrUnsupportedEvent) {
		t.Fatalf("unpaid checkout error = %v, want ErrUnsupportedEvent", err)
	}
	asyncPaid := []byte(`{"id":"evt_5","type":"checkout.session.async_payment_succeeded","created":1759319999,"data":{"object":{"id":"cs_1","amount_total":3000,"currency":"cny","payment_status":"paid","metadata":{"orderId":"ord-001"}}}}`)
	event, err := provider.VerifyWebhook(context.Background(), asyncPaid, stripeHeader(stripeTestSecret, asyncPaid, now.Unix()))
	if err != nil || event.Kind != model.PaymentSettled {
		t.Fatalf("async paid checkout = %+v, %v", event, err)
	}
}

func TestStripeAcceptsOneValidSignatureAmongSeveral(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	provider := NewStripeCompatible(StripeConfig{Secret: stripeTestSecret, Now: func() time.Time { return now }})
	body := []byte(`{"id":"evt_1","type":"checkout.session.completed","created":1759319999,"data":{"object":{"id":"cs_1","amount_total":3000,"currency":"cny","metadata":{"orderId":"ord-001"}}}}`)
	valid := stripeSignature(stripeTestSecret, body, now.Unix())
	timestamp, signature, _ := strings.Cut(valid, ",")
	header := http.Header{}
	header.Set(stripeSignatureHeader, timestamp+",v1="+strings.Repeat("00", 32)+","+signature)
	if _, err := provider.VerifyWebhook(context.Background(), body, header); err != nil {
		t.Fatalf("multi-signature header: %v", err)
	}
}
