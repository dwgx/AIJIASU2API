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
		Provider: model.ProviderEpay,
		State:    model.OrderAwaitingPayment,
	}
}

// cloneEpayForm copies a form so a table case can mutate it without touching
// the shared fixture.
func cloneEpayForm(form url.Values) url.Values {
	cloned := url.Values{}
	for key, list := range form {
		cloned[key] = append([]string(nil), list...)
	}
	return cloned
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

	// Callback URLs are optional: omitting both keeps them out of the signed
	// query instead of failing checkout.
	payURL, err = provider.Checkout(order, "wxpay", "", "")
	if err != nil {
		t.Fatalf("poll-only checkout: %v", err)
	}
	parsed, err = url.Parse(payURL)
	if err != nil {
		t.Fatalf("parse poll-only pay URL: %v", err)
	}
	pollQuery := parsed.Query()
	if pollQuery.Has("notify_url") || pollQuery.Has("return_url") {
		t.Fatalf("poll-only checkout carried callback URLs: %s", payURL)
	}
	if got := SignMD5(pollQuery, epayTestKey); got != pollQuery.Get("sign") {
		t.Fatalf("poll-only query signature does not verify")
	}

	provider.notifyURL = "https://panel.example.com/v1/payments/epay/notify"
	provider.returnURL = "https://panel.example.com/portal"
	payURL, err = provider.Checkout(order, "wxpay", "", "")
	if err != nil || !strings.Contains(payURL, "type=wxpay") {
		t.Fatalf("wxpay checkout = %q, %v", payURL, err)
	}
	parsed, _ = url.Parse(payURL)
	query = parsed.Query()
	if query.Get("notify_url") != provider.notifyURL || query.Get("return_url") != provider.returnURL {
		t.Fatalf("configured callback URLs missing: %s", payURL)
	}
	if _, err := provider.Checkout(order, "card", "", ""); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("unknown type error = %v, want ErrInvalidEvent", err)
	}
}

func TestEpayCheckoutOptionalCallbackURLs(t *testing.T) {
	order := testEpayOrder()
	coreOnly := NewEpay(EpayConfig{
		Gateway: "https://pay.example.test/", PID: "1001", Key: epayTestKey,
	})
	if !coreOnly.Enabled() || !coreOnly.Configured() {
		t.Fatalf("core-only config enabled=%t configured=%t", coreOnly.Enabled(), coreOnly.Configured())
	}
	payURL, err := coreOnly.Checkout(order, "alipay", "", "")
	if err != nil {
		t.Fatalf("core-only Checkout: %v", err)
	}
	parsed, err := url.Parse(payURL)
	if err != nil {
		t.Fatalf("parse core-only pay URL: %v", err)
	}
	query := parsed.Query()
	for _, key := range []string{"notify_url", "return_url"} {
		if query.Has(key) {
			t.Fatalf("core-only checkout includes %s: %s", key, payURL)
		}
	}
	if got := SignMD5(query, epayTestKey); got != query.Get("sign") {
		t.Fatalf("core-only query signature does not verify")
	}
	if _, err := coreOnly.Checkout(order, "alipay", "not-a-url", ""); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid notify URL error = %v, want ErrInvalidEvent", err)
	}

	// Explicit config-level overrides are included when set.
	withCallbacks := NewEpay(EpayConfig{
		Gateway:   "https://pay.example.test/",
		PID:       "1001",
		Key:       epayTestKey,
		NotifyURL: "https://panel.example.com/v1/payments/epay/notify",
		ReturnURL: "https://panel.example.com/portal",
	})
	payURL, err = withCallbacks.Checkout(order, "alipay", "", "")
	if err != nil {
		t.Fatalf("configured Checkout: %v", err)
	}
	parsed, _ = url.Parse(payURL)
	query = parsed.Query()
	if query.Get("notify_url") != withCallbacks.NotifyURL() ||
		query.Get("return_url") != withCallbacks.ReturnURL() {
		t.Fatalf("configured callback URLs missing: %s", payURL)
	}
	if got := SignMD5(query, epayTestKey); got != query.Get("sign") {
		t.Fatalf("configured query signature does not verify")
	}

	// One callback alone is valid; the other stays absent.
	notifyOnly := NewEpay(EpayConfig{
		Gateway:   "https://pay.example.test/",
		PID:       "1001",
		Key:       epayTestKey,
		NotifyURL: "https://panel.example.com/v1/payments/epay/notify",
	})
	payURL, err = notifyOnly.Checkout(order, "alipay", "", "")
	if err != nil {
		t.Fatalf("notify-only Checkout: %v", err)
	}
	parsed, _ = url.Parse(payURL)
	query = parsed.Query()
	if query.Get("notify_url") == "" || query.Has("return_url") {
		t.Fatalf("notify-only callbacks = %v", query)
	}
}

// TestEpayCheckoutStripsReturnURLQuery pins AJS-424 at the source: return_url
// travels inside the same unescaped canonical string a notify uses, so a
// client-supplied query would be signed as if it were callback vocabulary.
func TestEpayCheckoutStripsReturnURLQuery(t *testing.T) {
	provider := newTestEpay()
	order := testEpayOrder()
	const dirty = "https://panel.example.com/portal?trade_status=TRADE_SUCCESS&x=1"
	payURL, err := provider.Checkout(order, "alipay",
		"https://panel.example.com/v1/payments/epay/notify", dirty)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	parsed, err := url.Parse(payURL)
	if err != nil {
		t.Fatalf("parse pay URL: %v", err)
	}
	query := parsed.Query()
	clean := query.Get("return_url")
	if clean != "https://panel.example.com/portal" {
		t.Fatalf("return_url = %q, want the queryless path", clean)
	}
	if strings.ContainsAny(clean, "?#&") || strings.Contains(clean, "trade_status") {
		t.Fatalf("return_url kept client parameters: %q", clean)
	}
	if strings.Contains(payURL, "trade_status") {
		t.Fatalf("pay URL carries the smuggled pair: %s", payURL)
	}
	if got := SignMD5(query, epayTestKey); got != query.Get("sign") {
		t.Fatalf("signature does not verify over the cleaned query")
	}

	// The signed checkout query is not a notify: return_url and notify_url are
	// outside the callback vocabulary, so replaying it settles nothing.
	if _, err := provider.VerifyNotify(context.Background(), query, order); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("checkout replay error = %v, want ErrInvalidEvent", err)
	}

	// A fragment is stripped the same way and the path survives.
	payURL, err = provider.Checkout(order, "alipay", "", "https://panel.example.com/portal#frag")
	if err != nil {
		t.Fatalf("fragment Checkout: %v", err)
	}
	parsed, _ = url.Parse(payURL)
	if got := parsed.Query().Get("return_url"); got != "https://panel.example.com/portal" {
		t.Fatalf("fragment return_url = %q", got)
	}

	// A configured return URL goes through the same clean-up, and a stale
	// query must not disable the adapter at startup.
	configured := NewEpay(EpayConfig{
		Gateway: "https://pay.example.test/", PID: "1001", Key: epayTestKey,
		ReturnURL: dirty,
	})
	if !configured.Enabled() {
		t.Fatalf("dirty configured return URL disabled epay: %s", configured.DisabledReason())
	}
	if got := configured.ReturnURL(); got != "https://panel.example.com/portal" {
		t.Fatalf("normalized ReturnURL() = %q", got)
	}
	payURL, err = configured.Checkout(order, "alipay", "", "")
	if err != nil {
		t.Fatalf("configured Checkout: %v", err)
	}
	parsed, _ = url.Parse(payURL)
	if got := parsed.Query().Get("return_url"); got != "https://panel.example.com/portal" {
		t.Fatalf("configured return_url = %q", got)
	}
	if got := SignMD5(parsed.Query(), epayTestKey); got != parsed.Query().Get("sign") {
		t.Fatalf("configured query signature does not verify")
	}
	// A configured value that is not an absolute http(s) URL still disables
	// the adapter instead of shipping a broken callback.
	if bad := NewEpay(EpayConfig{
		Gateway: "https://pay.example.test/", PID: "1001", Key: epayTestKey,
		ReturnURL: "not-a-url",
	}); bad.Enabled() || !strings.Contains(bad.DisabledReason(), "AJS_EPAY_RETURN_URL") {
		t.Fatalf("bad configured return URL enabled=%t reason=%q", bad.Enabled(), bad.DisabledReason())
	}

	// notify_url is server-derived and is not rewritten, but it keeps its
	// existing separator rejection.
	if _, err := provider.Checkout(order, "alipay",
		"https://panel.example.com/v1/payments/epay/notify?a=1&b=2", ""); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("notify_url with a separator error = %v, want ErrInvalidEvent", err)
	}
	// A return URL that is not an absolute http(s) URL is still refused.
	if _, err := provider.Checkout(order, "alipay", "", "javascript:alert(1)"); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("javascript return_url error = %v, want ErrInvalidEvent", err)
	}
}

func TestEpayCheckoutRejectsNonCNY(t *testing.T) {
	order := testEpayOrder()
	order.Plan.Currency = "USD"
	_, err := newTestEpay().Checkout(order, "alipay", "", "")
	if !errors.Is(err, ErrUnsupportedCurrency) || !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("non-CNY checkout error = %v, want ErrUnsupportedCurrency/ErrInvalidEvent", err)
	}
}

func TestEpayGatewayRequiresHTTPS(t *testing.T) {
	cases := []struct {
		name    string
		gateway string
		want    bool
	}{
		{"plaintext hostname", "http://pay.example.test/", false},
		{"plaintext public ip", "http://192.0.2.10/", false},
		{"lookalike localhost", "http://localhost.example.test/", false},
		{"loopback ipv4", "http://127.0.0.1:18080/", true},
		{"loopback ipv6", "http://[::1]:18080/", true},
		{"localhost", "http://localhost:18080/", true},
		{"https", "https://pay.example.test/", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewEpay(EpayConfig{Gateway: tc.gateway, PID: "1001", Key: epayTestKey})
			if got := provider.Enabled(); got != tc.want {
				t.Fatalf("Enabled() = %t, want %t (reason %q)", got, tc.want, provider.DisabledReason())
			}
			if !tc.want && !strings.Contains(provider.DisabledReason(), "https") {
				t.Fatalf("disabled reason = %q, want an https explanation", provider.DisabledReason())
			}
		})
	}
}

func TestEpayNotifyRejectsProviderMismatchAndNonCNY(t *testing.T) {
	provider := newTestEpay()
	order := testEpayOrder()
	form := signedEpayNotify(t, epayTestKey, order, "trade-policy", "30.00")

	manual := order
	manual.Provider = model.ProviderManual
	if _, err := provider.VerifyNotify(context.Background(), form, manual); !errors.Is(err, ErrProviderMismatch) {
		t.Fatalf("manual order notify error = %v, want ErrProviderMismatch", err)
	}

	usd := order
	usd.Plan.Currency = "USD"
	if _, err := provider.VerifyNotify(context.Background(), form, usd); !errors.Is(err, ErrUnsupportedCurrency) {
		t.Fatalf("USD order notify error = %v, want ErrUnsupportedCurrency", err)
	}
}

func TestEpayDisabledOnlyWithoutCoreConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  EpayConfig
		want bool
	}{
		{"empty", EpayConfig{}, false},
		{"missing gateway", EpayConfig{PID: "1001", Key: epayTestKey}, false},
		{"missing pid", EpayConfig{Gateway: "https://pay.example.test/", Key: epayTestKey}, false},
		{"missing key", EpayConfig{Gateway: "https://pay.example.test/", PID: "1001"}, false},
		{"core only", EpayConfig{Gateway: "https://pay.example.test/", PID: "1001", Key: epayTestKey}, true},
		{"core with callbacks", EpayConfig{
			Gateway:   "https://pay.example.test/",
			PID:       "1001",
			Key:       epayTestKey,
			NotifyURL: "https://panel.example.com/v1/payments/epay/notify",
			ReturnURL: "https://panel.example.com/portal",
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewEpay(tc.cfg)
			if got := provider.Enabled(); got != tc.want {
				t.Fatalf("Enabled() = %t, want %t (reason %q)", got, tc.want, provider.DisabledReason())
			}
		})
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

// TestEpayNotifyRejectsFieldsOutsideProtocol closes the AJS-424 replay: a valid
// signature only proves the merchant key signed some field set, so a callback
// may only carry the fields 易支付 actually sends.
func TestEpayNotifyRejectsFieldsOutsideProtocol(t *testing.T) {
	provider := newTestEpay()
	order := testEpayOrder()
	canonical := signedEpayNotify(t, epayTestKey, order, "trade-canonical", "30.00")
	clone := func() url.Values { return cloneEpayForm(canonical) }

	// The canonical notify still settles.
	event, err := provider.VerifyNotify(context.Background(), clone(), order)
	if err != nil {
		t.Fatalf("canonical notify rejected: %v", err)
	}
	if event.ID != "trade-canonical" || event.Kind != model.PaymentSettled {
		t.Fatalf("canonical event = %+v", event)
	}

	// The documented optional fields are accepted: a real gateway payload
	// carries api_trade_no, addtime, endtime, buyer and timestamp, and the
	// protocol requires verification to tolerate them.
	optional := map[string]string{
		"api_trade_no": "40001249985198893",
		"addtime":      "2024-07-01 16:47:32",
		"endtime":      "2024-07-01 16:49:24",
		"buyer":        "openid-42",
		"timestamp":    "1721206072",
		"param":        "tenant-42",
	}
	documented := clone()
	for key, value := range optional {
		documented.Set(key, value)
	}
	documented.Set("sign", SignMD5(documented, epayTestKey))
	if _, err := provider.VerifyNotify(context.Background(), documented, order); err != nil {
		t.Fatalf("documented optional fields rejected: %v", err)
	}
	// Each optional field is also accepted on its own.
	for key, value := range optional {
		t.Run("optional_"+key, func(t *testing.T) {
			form := clone()
			form.Set(key, value)
			form.Set("sign", SignMD5(form, epayTestKey))
			if _, err := provider.VerifyNotify(context.Background(), form, order); err != nil {
				t.Fatalf("optional field %q rejected: %v", key, err)
			}
		})
	}

	// Any field outside the notify vocabulary is refused even when the caller
	// re-signs, which is the only way a promoted checkout query could carry a
	// success status. notify_url and return_url exist only in the checkout
	// query, so they are exactly the replay signature.
	for _, key := range []string{"return_url", "notify_url", "trade_status_extra", "x"} {
		t.Run(key, func(t *testing.T) {
			form := clone()
			form.Set(key, "TRADE_SUCCESS")
			form.Set("sign", SignMD5(form, epayTestKey))
			if _, err := provider.VerifyNotify(context.Background(), form, order); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("unknown key %q error = %v, want ErrInvalidEvent", key, err)
			}
		})
	}

	// The allowlist runs after the signature check: an unsigned extra key is a
	// signature failure, not an event-shape failure.
	stale := clone()
	stale.Set("x", "1")
	if _, err := provider.VerifyNotify(context.Background(), stale, order); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("unsigned extra key error = %v, want ErrInvalidSignature", err)
	}
}

// TestEpayReturnURLAndNotifyKeyEdges pins the adversarial boundaries of the
// AJS-424 cleaner and the case-sensitive notify vocabulary: only bytes that
// cannot become a canonical separator survive cleaning, and no case variant of
// a non-protocol key can slip through the allowlist.
func TestEpayReturnURLAndNotifyKeyEdges(t *testing.T) {
	provider := NewEpay(EpayConfig{
		Gateway: "https://pay.example.test/", PID: "1001", Key: epayTestKey,
	})
	order := testEpayOrder()

	cleanReturnURL := func(t *testing.T, raw string) (string, bool) {
		t.Helper()
		payURL, err := provider.Checkout(order, "alipay", "", raw)
		if errors.Is(err, ErrInvalidEvent) {
			return "", false
		}
		if err != nil {
			t.Fatalf("Checkout(%q): %v", raw, err)
		}
		parsed, err := url.Parse(payURL)
		if err != nil {
			t.Fatalf("parse pay URL for %q: %v", raw, err)
		}
		query := parsed.Query()
		if got := SignMD5(query, epayTestKey); got != query.Get("sign") {
			t.Fatalf("Checkout(%q) signature does not verify", raw)
		}
		return query.Get("return_url"), true
	}

	accepted := []struct {
		name string
		raw  string
		want string
	}{
		{"query", "https://panel.example.com/portal?trade_status=TRADE_SUCCESS&x=1", "https://panel.example.com/portal"},
		{"fragment", "https://panel.example.com/portal#frag", "https://panel.example.com/portal"},
		// Encoded separators are path bytes, not canonical separators: they
		// survive verbatim and cannot introduce a parameter.
		{"encoded amp", "https://panel.example.com/portal%26trade_status=TRADE_SUCCESS", "https://panel.example.com/portal%26trade_status=TRADE_SUCCESS"},
		{"double encoded amp", "https://panel.example.com/portal%2526trade_status=TRADE_SUCCESS", "https://panel.example.com/portal%2526trade_status=TRADE_SUCCESS"},
		{"encoded question", "https://panel.example.com/portal%3Fx=1", "https://panel.example.com/portal%3Fx=1"},
		// Empty after cleaning is the origin or root, never an empty value.
		{"bare question", "https://panel.example.com?", "https://panel.example.com"},
		{"bare fragment", "https://panel.example.com/#", "https://panel.example.com/"},
	}
	for _, tc := range accepted {
		t.Run("accept_"+tc.name, func(t *testing.T) {
			got, ok := cleanReturnURL(t, tc.raw)
			if !ok {
				t.Fatalf("Checkout(%q) rejected an acceptable return URL", tc.raw)
			}
			if got != tc.want {
				t.Fatalf("cleaned return_url = %q, want %q", got, tc.want)
			}
			if strings.ContainsAny(got, "?#") || strings.Contains(got, "&") {
				t.Fatalf("cleaned return_url carries a canonical separator: %q", got)
			}
		})
	}

	rejected := []struct {
		name string
		raw  string
	}{
		{"literal amp", "https://panel.example.com/a&b"},
		{"userinfo", "https://user:pass@panel.example.com/portal"},
		{"crlf", "https://panel.example.com/portal\r\nX: y"},
		{"javascript scheme", "javascript:alert(1)"},
		{"ftp scheme", "ftp://panel.example.com/portal"},
		{"relative path", "/portal"},
	}
	for _, tc := range rejected {
		t.Run("reject_"+tc.name, func(t *testing.T) {
			if _, ok := cleanReturnURL(t, tc.raw); ok {
				t.Fatalf("Checkout(%q) accepted a return URL it must refuse", tc.raw)
			}
		})
	}

	// The allowlist is case sensitive: a case variant is an unknown key even
	// though its value is harmless, and it is rejected after a valid signature.
	canonical := signedEpayNotify(t, epayTestKey, order, "trade-edge", "30.00")
	for _, key := range []string{"Return_URL", "Notify_URL", "PARAM", "SIGN_TYPE", "Trade_Status"} {
		t.Run("case_"+key, func(t *testing.T) {
			form := cloneEpayForm(canonical)
			form.Set(key, "TRADE_SUCCESS")
			form.Set("sign", SignMD5(form, epayTestKey))
			if _, err := provider.VerifyNotify(context.Background(), form, order); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("case variant %q error = %v, want ErrInvalidEvent", key, err)
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
