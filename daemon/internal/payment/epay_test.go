package payment

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

const epayTestKey = "test-secret-key"

func newTestEpay() *Epay {
	return NewEpay(EpayConfig{
		Gateway:  "https://pay.example.test/",
		PID:      "1001",
		Key:      epayTestKey,
		SignType: "MD5",
		Now: func() time.Time {
			return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
		},
	})
}

func testEpayOrder() model.Order {
	return model.Order{
		ID: "ord-001",
		Plan: model.PlanSnapshot{
			ID:          "plan-1",
			Name:        "Monthly 5 GiB",
			AmountMinor: 3000,
			Currency:    "CNY",
		},
		Provider: model.ProviderManual,
		State:    model.OrderAwaitingPayment,
	}
}

func signedEpayNotify(t *testing.T, key string, order model.Order, tradeNo, money string) url.Values {
	t.Helper()
	form := url.Values{
		"pid":          {"1001"},
		"trade_no":     {tradeNo},
		"out_trade_no": {order.ID},
		"type":         {"alipay"},
		"name":         {order.Plan.Name},
		"money":        {money},
		"trade_status": {"TRADE_SUCCESS"},
		"sign_type":    {"MD5"},
	}
	form.Set("sign", SignMD5(form, key))
	return form
}

func TestEpaySignCanonicalVector(t *testing.T) {
	values := url.Values{
		"pid":          {"1001"},
		"type":         {"alipay"},
		"out_trade_no": {"ord-001"},
		"notify_url":   {"https://panel.example.com/v1/payments/epay/notify"},
		"return_url":   {"https://panel.example.com/portal/orders"},
		"name":         {"Monthly 5 GiB"},
		"money":        {"30.00"},
		"sign_type":    {"MD5"},
		"sign":         {"ignored"},
		"empty":        {""},
	}
	const canonical = "money=30.00&name=Monthly 5 GiB&notify_url=https://panel.example.com/v1/payments/epay/notify&out_trade_no=ord-001&pid=1001&return_url=https://panel.example.com/portal/orders&type=alipay"
	if got := CanonicalEpayString(values); got != canonical {
		t.Fatalf("canonical string = %q, want %q", got, canonical)
	}
	const want = "2bc86124599cbc9a232826bb4da766ea"
	if got := SignMD5(values, epayTestKey); got != want {
		t.Fatalf("sign = %q, want %q", got, want)
	}

	tampered := url.Values{}
	for key, list := range values {
		tampered[key] = append([]string(nil), list...)
	}
	tampered.Set("money", "0.01")
	if got := SignMD5(tampered, epayTestKey); got == want {
		t.Fatalf("tampered amount kept the original signature")
	}
	if got := SignMD5(values, "another-key"); got == want {
		t.Fatalf("different key kept the original signature")
	}
}

func TestEpayCheckoutURLShape(t *testing.T) {
	provider := newTestEpay()
	order := testEpayOrder()
	payURL, err := provider.Checkout(order, "alipay",
		"https://panel.example.com/v1/payments/epay/notify",
		"https://panel.example.com/portal")
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	parsed, err := url.Parse(payURL)
	if err != nil {
		t.Fatalf("parse pay URL: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "pay.example.test" || parsed.Path != "/submit.php" {
		t.Fatalf("pay URL endpoint = %s://%s%s", parsed.Scheme, parsed.Host, parsed.Path)
	}
	query := parsed.Query()
	want := map[string]string{
		"pid":          "1001",
		"type":         "alipay",
		"out_trade_no": "ord-001",
		"notify_url":   "https://panel.example.com/v1/payments/epay/notify",
		"return_url":   "https://panel.example.com/portal",
		"name":         "Monthly 5 GiB",
		"money":        "30.00",
		"sign_type":    "MD5",
	}
	for key, value := range want {
		if got := query.Get(key); got != value {
			t.Fatalf("query %s = %q, want %q", key, got, value)
		}
	}
	if got := query.Get("sign"); len(got) != 32 {
		t.Fatalf("sign = %q, want 32 hex characters", got)
	}
	if got := SignMD5(query, epayTestKey); got != query.Get("sign") {
		t.Fatalf("query signature does not verify: %q vs %q", got, query.Get("sign"))
	}
	if strings.Contains(payURL, epayTestKey) {
		t.Fatalf("pay URL leaks the merchant key: %s", payURL)
	}

	if _, err := provider.Checkout(order, "wxpay", "", ""); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("missing callback URLs error = %v, want ErrInvalidEvent", err)
	}
	provider.notifyURL = "https://panel.example.com/v1/payments/epay/notify"
	provider.returnURL = "https://panel.example.com/portal"
	payURL, err = provider.Checkout(order, "wxpay", "", "")
	if err != nil || !strings.Contains(payURL, "type=wxpay") {
		t.Fatalf("wxpay checkout = %q, %v", payURL, err)
	}
	if _, err := provider.Checkout(order, "card", "", ""); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("unknown type error = %v, want ErrInvalidEvent", err)
	}
}

func TestEpayCheckoutResolvesGatewayWithPath(t *testing.T) {
	provider := NewEpay(EpayConfig{
		Gateway: "https://pay.example.test/pay/submit.php", PID: "1001", Key: epayTestKey,
	})
	payURL, err := provider.Checkout(testEpayOrder(), "alipay",
		"https://panel.example.com/v1/payments/epay/notify",
		"https://panel.example.com/portal")
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	parsed, _ := url.Parse(payURL)
	if parsed.Path != "/pay/submit.php" {
		t.Fatalf("submit path = %q", parsed.Path)
	}
	queryURL, err := provider.endpoint("api.php")
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	if queryURL.Path != "/pay/api.php" {
		t.Fatalf("query path = %q", queryURL.Path)
	}
}

func TestEpayNotifyVerificationMatrix(t *testing.T) {
	provider := newTestEpay()
	order := testEpayOrder()
	valid := signedEpayNotify(t, epayTestKey, order, "trade-9001", "30.00")

	event, err := provider.VerifyNotify(context.Background(), valid, order)
	if err != nil {
		t.Fatalf("VerifyNotify: %v", err)
	}
	if event.Provider != model.ProviderEpay || event.ID != "trade-9001" ||
		event.ProviderRef != "trade-9001" || event.Kind != model.PaymentSettled ||
		event.OrderID != order.ID || event.AmountMinor != 3000 || event.Currency != "CNY" {
		t.Fatalf("event = %+v", event)
	}
	if event.OccurredAt != "2026-10-25T12:00:00Z" {
		t.Fatalf("occurredAt = %q", event.OccurredAt)
	}

	resign := func(mutate func(url.Values)) url.Values {
		form := url.Values{}
		for key, list := range valid {
			form[key] = append([]string(nil), list...)
		}
		mutate(form)
		form.Set("sign", SignMD5(form, epayTestKey))
		return form
	}
	cases := []struct {
		name string
		form url.Values
		want error
	}{
		{"wrong sign", func() url.Values {
			form := url.Values{}
			for key, list := range valid {
				form[key] = append([]string(nil), list...)
			}
			form.Set("sign", strings.Repeat("0", 32))
			return form
		}(), ErrInvalidSignature},
		{"wrong pid", resign(func(form url.Values) { form.Set("pid", "9999") }), ErrInvalidEvent},
		{"wrong amount", resign(func(form url.Values) { form.Set("money", "0.01") }), ErrInvalidEvent},
		{"wrong status", resign(func(form url.Values) { form.Set("trade_status", "WAIT_BUYER_PAY") }), ErrUnsupportedEvent},
		{"wrong order", resign(func(form url.Values) { form.Set("out_trade_no", "ord-999") }), ErrInvalidEvent},
		{"missing trade no", resign(func(form url.Values) { form.Del("trade_no") }), ErrInvalidEvent},
		{"duplicate key", resign(func(form url.Values) { form.Add("pid", "1001") }), ErrInvalidEvent},
		{"bad sign type", resign(func(form url.Values) { form.Set("sign_type", "SHA256") }), ErrInvalidSignature},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := provider.VerifyNotify(context.Background(), tc.form, order); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEpayDisabledWithoutCompleteConfig(t *testing.T) {
	empty := NewEpay(EpayConfig{})
	if empty.Enabled() || empty.Configured() {
		t.Fatalf("empty config reports enabled/configured")
	}
	if _, err := empty.Checkout(testEpayOrder(), "alipay", "https://a.test/n", "https://a.test/r"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled Checkout error = %v", err)
	}
	if _, err := empty.VerifyNotify(context.Background(), url.Values{}, testEpayOrder()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled VerifyNotify error = %v", err)
	}
	if _, err := empty.QueryOrder(context.Background(), "ord-001"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled QueryOrder error = %v", err)
	}

	partial := NewEpay(EpayConfig{Gateway: "https://pay.example.test/"})
	if partial.Enabled() || !partial.Configured() {
		t.Fatalf("partial config enabled=%t configured=%t", partial.Enabled(), partial.Configured())
	}
	if !strings.Contains(partial.DisabledReason(), "must all be set") {
		t.Fatalf("partial reason = %q", partial.DisabledReason())
	}
	badSign := NewEpay(EpayConfig{
		Gateway: "https://pay.example.test/", PID: "1", Key: "k", SignType: "SHA256",
	})
	if badSign.Enabled() || !strings.Contains(badSign.DisabledReason(), "MD5") {
		t.Fatalf("bad sign type reason = %q", badSign.DisabledReason())
	}
	badGateway := NewEpay(EpayConfig{Gateway: "ftp://pay.example.test/", PID: "1", Key: "k"})
	if badGateway.Enabled() || !strings.Contains(badGateway.DisabledReason(), "http") {
		t.Fatalf("bad gateway reason = %q", badGateway.DisabledReason())
	}
	valid := newTestEpay()
	if !valid.Enabled() || valid.GatewayHost() != "pay.example.test" {
		t.Fatalf("valid provider enabled=%t host=%q", valid.Enabled(), valid.GatewayHost())
	}
}

func TestEpayMoneyFormatting(t *testing.T) {
	for amount, want := range map[int64]string{1: "0.01", 100: "1.00", 3000: "30.00", 123456: "1234.56"} {
		if got := FormatMoneyMinor(amount); got != want {
			t.Fatalf("FormatMoneyMinor(%d) = %q, want %q", amount, got, want)
		}
	}
	for raw, want := range map[string]int64{"30.00": 3000, "30": 3000, "30.0": 3000, "0.01": 1} {
		got, err := ParseMoneyMinor(raw)
		if err != nil || got != want {
			t.Fatalf("ParseMoneyMinor(%q) = %d, %v, want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "0.00", "-1.00", "+1.00", "1e2", "30.001", "abc", "1,00"} {
		if _, err := ParseMoneyMinor(raw); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("ParseMoneyMinor(%q) error = %v, want ErrInvalidEvent", raw, err)
		}
	}
}

func TestEpayQueryErrorDoesNotLeakKey(t *testing.T) {
	provider := NewEpay(EpayConfig{
		Gateway: "http://127.0.0.1:1/", PID: "1001", Key: "super-secret-epay-key",
	})
	_, err := provider.QueryOrder(context.Background(), "ord-001")
	if err == nil {
		t.Fatalf("QueryOrder unexpectedly succeeded")
	}
	text := fmt.Sprintf("%v", err)
	if strings.Contains(text, "super-secret-epay-key") || strings.Contains(text, "act=order") {
		t.Fatalf("query error leaks the key or request: %q", text)
	}
}
