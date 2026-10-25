package payment

import (
	"context"
	"crypto/md5" //nolint:gosec // 易支付 specifies MD5 over the canonical query string.
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// EpaySignTypeMD5 is the only signature algorithm 易支付 defines for the
// redirect + notify flow. A different configured value disables the adapter
// instead of silently signing with the wrong algorithm.
const EpaySignTypeMD5 = "MD5"

// Epay payment types accepted by checkout. The gateway decides which channels
// are enabled for the merchant; the daemon only refuses values it cannot name.
const (
	EpayTypeAlipay = "alipay"
	EpayTypeWxpay  = "wxpay"
)

// EpayConfig configures the 易支付 adapter. Gateway, PID and Key are the
// all-or-nothing core: if any one is missing the adapter stays disabled and
// every route answers 503. SignType defaults to MD5. NotifyURL and ReturnURL
// are optional overrides; when empty the API derives them from the configured
// tenant origin so the same binary works behind any portal hostname.
type EpayConfig struct {
	Gateway    string
	PID        string
	Key        string
	SignType   string
	NotifyURL  string
	ReturnURL  string
	HTTPClient *http.Client
	Now        func() time.Time
}

// Epay implements the 易支付 redirect checkout and signed form notify. It is a
// PaymentProvider; it never mutates an order itself, it only verifies facts and
// reduces them to one model.PaymentEvent for the store's single event path.
type Epay struct {
	gateway    *url.URL
	pid        string
	key        string
	signType   string
	notifyURL  string
	returnURL  string
	client     *http.Client
	now        func() time.Time
	configured bool
	problem    string
}

// NewEpay builds the adapter. A nil clock uses time.Now; a nil HTTP client gets
// a bounded ten-second client used by the order query path.
func NewEpay(cfg EpayConfig) *Epay {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	p := &Epay{
		pid:       strings.TrimSpace(cfg.PID),
		key:       strings.TrimSpace(cfg.Key),
		signType:  strings.ToUpper(strings.TrimSpace(cfg.SignType)),
		notifyURL: strings.TrimSpace(cfg.NotifyURL),
		returnURL: strings.TrimSpace(cfg.ReturnURL),
		client:    client,
		now:       now,
	}
	if p.signType == "" {
		p.signType = EpaySignTypeMD5
	}
	gateway := strings.TrimSpace(cfg.Gateway)
	p.configured = gateway != "" || p.pid != "" || p.key != "" ||
		strings.TrimSpace(cfg.SignType) != "" || p.notifyURL != "" || p.returnURL != ""
	if gateway == "" || p.pid == "" || p.key == "" {
		if p.configured {
			p.problem = "AJS_EPAY_GATEWAY, AJS_EPAY_PID and AJS_EPAY_KEY must all be set"
		}
		return p
	}
	if p.signType != EpaySignTypeMD5 {
		p.problem = "AJS_EPAY_SIGN_TYPE must be MD5"
		return p
	}
	if strings.ContainsAny(p.pid, "&=\r\n\t") {
		p.problem = "AJS_EPAY_PID must not contain separators or control characters"
		return p
	}
	parsed, err := url.Parse(gateway)
	if err != nil || parsed.Host == "" ||
		(strings.ToLower(parsed.Scheme) != "http" && strings.ToLower(parsed.Scheme) != "https") {
		p.problem = "AJS_EPAY_GATEWAY must be an absolute http(s) URL"
		return p
	}
	if parsed.User != nil {
		p.problem = "AJS_EPAY_GATEWAY must not carry credentials"
		return p
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		p.problem = "AJS_EPAY_GATEWAY must not carry a query or fragment"
		return p
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if p.notifyURL != "" && (!validHTTPURL(p.notifyURL) || strings.ContainsAny(p.notifyURL, "&\r\n\t")) {
		p.problem = "AJS_EPAY_NOTIFY_URL must be an absolute http(s) URL without query separators"
		return p
	}
	if p.returnURL != "" && (!validHTTPURL(p.returnURL) || strings.ContainsAny(p.returnURL, "&\r\n\t")) {
		p.problem = "AJS_EPAY_RETURN_URL must be an absolute http(s) URL without query separators"
		return p
	}
	p.gateway = parsed
	return p
}

// Name implements PaymentProvider.
func (p *Epay) Name() string { return model.ProviderEpay }

// Mode implements PaymentProvider. Epay uses a hosted checkout redirect and a
// signed server callback, so it belongs to the webhook-capable family.
func (p *Epay) Mode() string { return "webhook" }

// Enabled reports whether every required setting is present and valid.
func (p *Epay) Enabled() bool { return p != nil && p.gateway != nil && p.problem == "" }

// Configured reports whether any epay setting was supplied. It distinguishes a
// deliberately absent integration from a partial one worth warning about.
func (p *Epay) Configured() bool { return p != nil && p.configured }

// DisabledReason returns a stable, value-free reason for startup logs. It never
// contains the gateway URL, PID, key or any signature material.
func (p *Epay) DisabledReason() string {
	if p == nil || p.problem == "" {
		return "epay is not configured"
	}
	return p.problem
}

// GatewayHost is safe to log: it names the gateway host without its path,
// query or credentials. Empty while the adapter is disabled.
func (p *Epay) GatewayHost() string {
	if p == nil || p.gateway == nil {
		return ""
	}
	return p.gateway.Host
}

// NotifyURL returns the configured callback URL override. Empty means the API
// derives one from the tenant origin.
func (p *Epay) NotifyURL() string {
	if p == nil {
		return ""
	}
	return p.notifyURL
}

// ReturnURL returns the configured browser return URL override.
func (p *Epay) ReturnURL() string {
	if p == nil {
		return ""
	}
	return p.returnURL
}

// CanonicalEpayString builds the string 易支付 signs: every nonempty parameter
// except sign and sign_type, sorted by key, joined as key=value with "&". The
// values are not URL-encoded before hashing; that is the gateway's documented
// rule, and it is why the same helper serves checkout and notify verification.
func CanonicalEpayString(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key, list := range values {
		if key == "sign" || key == "sign_type" {
			continue
		}
		if len(list) == 0 || list[0] == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values.Get(key))
	}
	return strings.Join(parts, "&")
}

// SignMD5 returns the lowercase hex MD5 of the canonical string plus the
// merchant key. MD5 is required by the 易支付 protocol; it is not a general
// purpose choice and the adapter never exposes the key.
func SignMD5(values url.Values, key string) string {
	sum := md5.Sum([]byte(CanonicalEpayString(values) + key))
	return hex.EncodeToString(sum[:])
}

// Checkout builds the hosted payment URL. It carries only public order data:
// the merchant id, the order id, the amount formatted with two decimals, the
// plan name and the two callback URLs. The merchant key and the signature are
// never logged.
func (p *Epay) Checkout(order model.Order, kind, notifyURL, returnURL string) (string, error) {
	if !p.Enabled() {
		return "", ErrDisabled
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != EpayTypeAlipay && kind != EpayTypeWxpay {
		return "", ErrInvalidEvent
	}
	orderID := strings.TrimSpace(order.ID)
	name := strings.TrimSpace(order.Plan.Name)
	// The gateway signs raw key=value pairs without escaping, so a separator
	// inside a value would make the canonical string ambiguous.
	if orderID == "" || name == "" || order.Plan.AmountMinor <= 0 ||
		strings.ContainsAny(orderID, "&=\r\n\t") || strings.ContainsAny(name, "&\r\n\t") {
		return "", ErrInvalidEvent
	}
	notify := strings.TrimSpace(notifyURL)
	if notify == "" {
		notify = p.notifyURL
	}
	back := strings.TrimSpace(returnURL)
	if back == "" {
		back = p.returnURL
	}
	if !validHTTPURL(notify) || !validHTTPURL(back) ||
		strings.ContainsAny(notify, "&\r\n\t") || strings.ContainsAny(back, "&\r\n\t") {
		return "", ErrInvalidEvent
	}

	values := url.Values{}
	values.Set("pid", p.pid)
	values.Set("type", kind)
	values.Set("out_trade_no", orderID)
	values.Set("notify_url", notify)
	values.Set("return_url", back)
	values.Set("name", name)
	values.Set("money", FormatMoneyMinor(order.Plan.AmountMinor))
	values.Set("sign_type", p.signType)
	values.Set("sign", SignMD5(values, p.key))

	endpoint, err := p.endpoint("submit.php")
	if err != nil {
		return "", err
	}
	endpoint.RawQuery = values.Encode()
	return endpoint.String(), nil
}

// VerifyNotify verifies one form-encoded gateway callback against the order's
// immutable snapshot. The signature is checked over every received field
// before any value is trusted; PID, trade status, order id, trade number and
// the exact amount are then checked. A mismatch is an error and never becomes
// an event.
func (p *Epay) VerifyNotify(ctx context.Context, form url.Values, order model.Order) (model.PaymentEvent, error) {
	if err := ctx.Err(); err != nil {
		return model.PaymentEvent{}, err
	}
	if !p.Enabled() {
		return model.PaymentEvent{}, ErrDisabled
	}
	if form == nil {
		return model.PaymentEvent{}, ErrInvalidEvent
	}
	// Duplicate keys would make the canonical string depend on map iteration
	// order in a way the gateway never signs. Reject them outright.
	for _, values := range form {
		if len(values) != 1 {
			return model.PaymentEvent{}, ErrInvalidEvent
		}
	}
	signature := strings.ToLower(strings.TrimSpace(form.Get("sign")))
	if signature == "" {
		return model.PaymentEvent{}, ErrInvalidSignature
	}
	if raw := strings.TrimSpace(form.Get("sign_type")); raw != "" &&
		!strings.EqualFold(raw, EpaySignTypeMD5) {
		return model.PaymentEvent{}, ErrInvalidSignature
	}
	expected := SignMD5(form, p.key)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return model.PaymentEvent{}, ErrInvalidSignature
	}
	if subtle.ConstantTimeCompare(
		[]byte(strings.TrimSpace(form.Get("pid"))), []byte(p.pid)) != 1 {
		return model.PaymentEvent{}, ErrInvalidEvent
	}
	if !strings.EqualFold(strings.TrimSpace(form.Get("trade_status")), "TRADE_SUCCESS") {
		return model.PaymentEvent{}, ErrUnsupportedEvent
	}
	outTradeNo := strings.TrimSpace(form.Get("out_trade_no"))
	if outTradeNo == "" || outTradeNo != order.ID {
		return model.PaymentEvent{}, ErrInvalidEvent
	}
	tradeNo := strings.TrimSpace(form.Get("trade_no"))
	if tradeNo == "" || len(tradeNo) > 128 || strings.ContainsAny(tradeNo, "\r\n\t") {
		return model.PaymentEvent{}, ErrInvalidEvent
	}
	amount, err := ParseMoneyMinor(form.Get("money"))
	if err != nil || amount != order.Plan.AmountMinor {
		return model.PaymentEvent{}, ErrInvalidEvent
	}
	return model.PaymentEvent{
		Provider:    model.ProviderEpay,
		ID:          tradeNo,
		Kind:        model.PaymentSettled,
		OrderID:     order.ID,
		ProviderRef: tradeNo,
		AmountMinor: order.Plan.AmountMinor,
		Currency:    order.Plan.Currency,
		OccurredAt:  p.now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// endpoint resolves the submit.php or api.php URL below the configured
// gateway. A gateway may be a bare origin, a directory, or a full submit.php.
func (p *Epay) endpoint(name string) (*url.URL, error) {
	if !p.Enabled() {
		return nil, ErrDisabled
	}
	u := *p.gateway
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, "/submit.php") {
		path = strings.TrimSuffix(path, "/submit.php")
	} else if strings.HasSuffix(path, "/api.php") {
		path = strings.TrimSuffix(path, "/api.php")
	}
	if path == "" {
		path = "/" + name
	} else {
		path = path + "/" + name
	}
	u.Path = path
	u.RawQuery = ""
	u.Fragment = ""
	return &u, nil
}

// FormatMoneyMinor formats an integer minor amount as the gateway's decimal
// string without using floating point (3000 -> "30.00").
func FormatMoneyMinor(amount int64) string {
	return fmt.Sprintf("%d.%02d", amount/100, amount%100)
}

// ParseMoneyMinor parses a gateway decimal amount into minor units. It rejects
// signs, exponents, more than two decimal places and values that would
// overflow; an amount must be positive.
func ParseMoneyMinor(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 20 || strings.HasPrefix(raw, "+") || strings.HasPrefix(raw, "-") {
		return 0, ErrInvalidEvent
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, ErrInvalidEvent
	}
	whole := parts[0]
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if len(fraction) > 2 {
			return 0, ErrInvalidEvent
		}
	}
	if len(whole) > 15 {
		return 0, ErrInvalidEvent
	}
	for _, digit := range whole {
		if digit < '0' || digit > '9' {
			return 0, ErrInvalidEvent
		}
	}
	for _, digit := range fraction {
		if digit < '0' || digit > '9' {
			return 0, ErrInvalidEvent
		}
	}
	for len(fraction) < 2 {
		fraction += "0"
	}
	wholeMinor, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, ErrInvalidEvent
	}
	fractionMinor, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, ErrInvalidEvent
	}
	amount := wholeMinor*100 + fractionMinor
	if amount <= 0 {
		return 0, ErrInvalidEvent
	}
	return amount, nil
}

func validHTTPURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return scheme == "http" || scheme == "https"
}
