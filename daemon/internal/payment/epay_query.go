package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// Query failures. ErrOrderNotFound means the gateway answered a well-formed
// "no such order" response; ErrQueryFailed covers transport, HTTP status and
// malformed payloads. Neither error ever embeds the request URL, which carries
// the merchant key.
var (
	ErrOrderNotFound = errors.New("epay order not found")
	ErrQueryFailed   = errors.New("epay order query failed")
	// ErrQueryAuth is a merchant credential/configuration rejection. It wraps
	// ErrQueryFailed so callers that only know the transport error still see
	// a query failure, while the reconciler can name the actual cause.
	ErrQueryAuth = fmt.Errorf("%w: authentication rejected", ErrQueryFailed)
	// ErrQueryTransient is a retryable gateway failure (busy, rate limit,
	// unknown non-1 code, 5xx). It is audited and retried on the next pass.
	ErrQueryTransient = fmt.Errorf("%w: transient gateway failure", ErrQueryFailed)
)

const (
	// DefaultEpayReconcileInterval is the documented 60-second poll cadence.
	DefaultEpayReconcileInterval = 60 * time.Second
	// defaultEpayReconcileBatch bounds one pass so a large backlog cannot pin
	// the goroutine or hammer the gateway.
	defaultEpayReconcileBatch = 50
	epayQueryTimeout          = 10 * time.Second
	epayQueryBodyLimit        = 64 << 10
)

// EpayOrderStatus is the bounded projection of the gateway's order query
// response. It carries no signature or raw payload.
type EpayOrderStatus struct {
	TradeNo     string
	OutTradeNo  string
	Type        string
	Name        string
	Money       string
	TradeStatus string
	// Status is the standard order-query integer: 0 unpaid, 1 paid,
	// 2 refunded. Some forks return trade_status instead; Paid accepts both.
	Status int
}

// Paid reports whether the gateway says the order is settled. The standard
// 易支付 query API returns status=1 and no trade_status field, while some
// forks keep the notify-style TRADE_SUCCESS value; either is authoritative.
func (s EpayOrderStatus) Paid() bool {
	if s.Status != 0 {
		return s.Status == 1
	}
	return strings.EqualFold(strings.TrimSpace(s.TradeStatus), "TRADE_SUCCESS")
}

// QueryOrder calls the gateway's api.php?act=order endpoint. The merchant key
// travels in the query string, so transport errors are deliberately reduced to
// a generic error instead of wrapping *url.Error (whose Error() includes the
// URL).
func (p *Epay) QueryOrder(ctx context.Context, outTradeNo string) (EpayOrderStatus, error) {
	if err := ctx.Err(); err != nil {
		return EpayOrderStatus{}, err
	}
	if !p.Enabled() {
		return EpayOrderStatus{}, ErrDisabled
	}
	outTradeNo = strings.TrimSpace(outTradeNo)
	if outTradeNo == "" {
		return EpayOrderStatus{}, ErrInvalidEvent
	}
	endpoint, err := p.endpoint("api.php")
	if err != nil {
		return EpayOrderStatus{}, err
	}
	query := endpoint.Query()
	query.Set("act", "order")
	query.Set("pid", p.pid)
	query.Set("key", p.key)
	query.Set("out_trade_no", outTradeNo)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return EpayOrderStatus{}, ErrQueryFailed
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return EpayOrderStatus{}, ctx.Err()
		}
		return EpayOrderStatus{}, fmt.Errorf("%w: transport error", ErrQueryFailed)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return EpayOrderStatus{}, fmt.Errorf("%w: status %d", ErrQueryFailed, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, epayQueryBodyLimit+1))
	if err != nil {
		return EpayOrderStatus{}, fmt.Errorf("%w: response read failed", ErrQueryFailed)
	}
	if len(body) > epayQueryBodyLimit {
		return EpayOrderStatus{}, fmt.Errorf("%w: response too large", ErrQueryFailed)
	}
	var payload epayQueryResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return EpayOrderStatus{}, fmt.Errorf("%w: malformed response", ErrQueryFailed)
	}
	if code := payload.code(); code != 1 {
		return EpayOrderStatus{}, epayQueryError(code, payload.Msg)
	}
	status := payload.status()
	if strings.TrimSpace(status.OutTradeNo) == "" {
		return EpayOrderStatus{}, fmt.Errorf("%w: missing out_trade_no", ErrQueryFailed)
	}
	return status, nil
}

// epayQueryResponse accepts both the flat response used by most 易支付
// deployments and the nested {"data":{...}} shape used by some forks.
type epayQueryResponse struct {
	Code        json.RawMessage `json:"code"`
	Msg         string          `json:"msg"`
	TradeNo     string          `json:"trade_no"`
	OutTradeNo  string          `json:"out_trade_no"`
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Money       string          `json:"money"`
	TradeStatus string          `json:"trade_status"`
	Status      json.RawMessage `json:"status"`
	Data        *epayQueryData  `json:"data"`
}

type epayQueryData struct {
	TradeNo     string          `json:"trade_no"`
	OutTradeNo  string          `json:"out_trade_no"`
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Money       string          `json:"money"`
	TradeStatus string          `json:"trade_status"`
	Status      json.RawMessage `json:"status"`
}

func (r epayQueryResponse) code() int { return gatewayInt(r.Code) }

// gatewayInt accepts the number and quoted-number shapes seen across 易支付
// deployments; an absent or malformed value reads as zero.
func gatewayInt(raw json.RawMessage) int {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0
	}
	text = strings.Trim(text, `"`)
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}
	return value
}

// epayQueryError maps the gateway's non-success code to a typed error. The
// documented meanings are -1 = order not found and -3 = merchant key error;
// other codes are transient. A few deployments use 0 plus a not-found or
// signature message, so the message is consulted only as a fallback.
func epayQueryError(code int, msg string) error {
	text := strings.TrimSpace(msg)
	lower := strings.ToLower(text)
	notFound := strings.Contains(text, "不存在") || strings.Contains(text, "未找到") ||
		strings.Contains(lower, "not found") || strings.Contains(lower, "no such order")
	auth := strings.Contains(text, "密钥") || strings.Contains(text, "签名") ||
		strings.Contains(lower, "merchant key") || strings.Contains(lower, "signature") ||
		strings.Contains(lower, "auth")
	switch {
	case code == -3:
		return fmt.Errorf("%w: gateway code %d", ErrQueryAuth, code)
	case code == -1:
		// -1 is the documented not-found code; a few gateways use it for a
		// signature failure instead, which the message disambiguates.
		if auth {
			return fmt.Errorf("%w: gateway code %d", ErrQueryAuth, code)
		}
		return ErrOrderNotFound
	case notFound:
		return ErrOrderNotFound
	case auth:
		return fmt.Errorf("%w: gateway code %d", ErrQueryAuth, code)
	default:
		return fmt.Errorf("%w: gateway code %d", ErrQueryTransient, code)
	}
}

func (r epayQueryResponse) status() EpayOrderStatus {
	status := EpayOrderStatus{
		TradeNo:     strings.TrimSpace(r.TradeNo),
		OutTradeNo:  strings.TrimSpace(r.OutTradeNo),
		Type:        strings.TrimSpace(r.Type),
		Name:        strings.TrimSpace(r.Name),
		Money:       strings.TrimSpace(r.Money),
		TradeStatus: strings.TrimSpace(r.TradeStatus),
		Status:      gatewayInt(r.Status),
	}
	if r.Data != nil {
		if status.TradeNo == "" {
			status.TradeNo = strings.TrimSpace(r.Data.TradeNo)
		}
		if status.OutTradeNo == "" {
			status.OutTradeNo = strings.TrimSpace(r.Data.OutTradeNo)
		}
		if status.Type == "" {
			status.Type = strings.TrimSpace(r.Data.Type)
		}
		if status.Name == "" {
			status.Name = strings.TrimSpace(r.Data.Name)
		}
		if status.Money == "" {
			status.Money = strings.TrimSpace(r.Data.Money)
		}
		if status.TradeStatus == "" {
			status.TradeStatus = strings.TrimSpace(r.Data.TradeStatus)
		}
		if status.Status == 0 && len(r.Data.Status) > 0 {
			status.Status = gatewayInt(r.Data.Status)
		}
	}
	return status
}

// EpayReconcileStore is the store surface the reconciler needs. Keeping it an
// interface makes the polling loop testable without a live gateway or store.
// PendingEpayOrders is the store's selection of rows that need a provider
// query: awaiting checkouts and paid orders whose entitlement was never
// granted. The store owns the state rules; the reconciler never invents them.
type EpayReconcileStore interface {
	PendingEpayOrders() []model.Order
	ApplyPaymentEvent(provider string, event model.PaymentEvent, actor string) (model.Order, error)
	RecordAudit(severity model.Severity, actor, action, target, detail string) error
	// DurabilityError reports a failed journal commit. A non-nil value stops
	// the pass: the store is poisoned and every later write is refused.
	DurabilityError() error
}

// EpayReconcilerConfig wires the poller. Interval defaults to 60s; MaxOrders
// defaults to 50 per pass. Jitter is injectable for deterministic tests.
type EpayReconcilerConfig struct {
	Provider  *Epay
	Store     EpayReconcileStore
	Logger    *slog.Logger
	Interval  time.Duration
	MaxOrders int
	Now       func() time.Time
	Jitter    func(time.Duration) time.Duration
}

// EpayReconciler polls the epay orders that still need a provider answer and
// settles the ones the gateway reports TRADE_SUCCESS. The store selects the
// rows: awaiting checkouts and paid orders whose entitlement was never granted
// are reconciled, while terminal and review-required rows are left alone. The
// poller is intentionally separate from the HTTP surface: a restart resumes
// reconciliation from the persisted order rows.
type EpayReconciler struct {
	provider  *Epay
	store     EpayReconcileStore
	logger    *slog.Logger
	interval  time.Duration
	maxOrders int
	now       func() time.Time
	jitter    func(time.Duration) time.Duration
	// next rotates the start of a full batch. Without it a backlog larger
	// than maxOrders would re-query the same oldest rows forever and never
	// reach a paid order behind them.
	next int
}

// NewEpayReconciler builds the poller. It returns nil when the provider or
// store is absent; callers treat a nil reconciler as disabled.
func NewEpayReconciler(cfg EpayReconcilerConfig) *EpayReconciler {
	if cfg.Provider == nil || cfg.Store == nil || !cfg.Provider.Enabled() {
		return nil
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = DefaultEpayReconcileInterval
	}
	maxOrders := cfg.MaxOrders
	if maxOrders <= 0 {
		maxOrders = defaultEpayReconcileBatch
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	jitter := cfg.Jitter
	if jitter == nil {
		jitter = jitteredEpayInterval
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &EpayReconciler{
		provider:  cfg.Provider,
		store:     cfg.Store,
		logger:    logger,
		interval:  interval,
		maxOrders: maxOrders,
		now:       now,
		jitter:    jitter,
	}
}

// Run polls until the context is cancelled. The first pass is delayed by one
// jittered interval so a restart storm does not hit the gateway at once.
func (r *EpayReconciler) Run(ctx context.Context) {
	if r == nil {
		return
	}
	interval := r.interval
	if interval <= 0 {
		interval = DefaultEpayReconcileInterval
	}
	jitter := r.jitter
	if jitter == nil {
		jitter = jitteredEpayInterval
	}
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	timer := time.NewTimer(jitter(interval))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := r.ReconcileOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("epay_reconcile_pass_failed", "error", err)
			}
			timer.Reset(jitter(interval))
		}
	}
}

// ReconcileOnce performs one bounded pass. A gateway error for one order is
// logged and does not stop the others; a disabled provider ends the pass with
// ErrDisabled.
func (r *EpayReconciler) ReconcileOnce(ctx context.Context) error {
	if r == nil || r.provider == nil || !r.provider.Enabled() {
		return ErrDisabled
	}
	if r.store == nil {
		return errors.New("epay reconciler store is not configured")
	}
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	now := r.now
	if now == nil {
		now = time.Now
	}
	orders := r.store.PendingEpayOrders()
	maxOrders := r.maxOrders
	if maxOrders <= 0 {
		maxOrders = defaultEpayReconcileBatch
	}
	if len(orders) > maxOrders {
		total := len(orders)
		start := r.next % total
		if start < 0 {
			start = 0
		}
		rotated := make([]model.Order, 0, total)
		rotated = append(rotated, orders[start:]...)
		rotated = append(rotated, orders[:start]...)
		orders = rotated[:maxOrders]
		r.next = (start + maxOrders) % total
	} else {
		r.next = 0
	}
	for _, order := range orders {
		if err := ctx.Err(); err != nil {
			return err
		}
		// A mutation or audit row that could not be committed poisons the
		// store; stop querying the gateway instead of accumulating
		// observations the journal cannot hold.
		if err := r.store.DurabilityError(); err != nil {
			return err
		}
		queryCtx, cancel := context.WithTimeout(ctx, epayQueryTimeout)
		status, err := r.provider.QueryOrder(queryCtx, order.ID)
		cancel()
		switch {
		case err == nil:
		case errors.Is(err, ErrOrderNotFound):
			// A normal "not paid yet / not created" answer; nothing to do.
			continue
		case errors.Is(err, ErrQueryAuth):
			// A wrong PID/key stops every query, so make it visible instead
			// of silently skipping the whole queue.
			logger.Error("epay_reconcile_auth_failed", "order_id", order.ID, "error", err)
			if auditErr := r.store.RecordAudit(model.SeverityWarn, "system", "epay.reconcile", order.ID,
				"gateway rejected the merchant credentials; check AJS_EPAY_PID/AJS_EPAY_KEY"); auditErr != nil {
				return auditErr
			}
			continue
		case errors.Is(err, ErrQueryTransient), errors.Is(err, ErrQueryFailed):
			logger.Warn("epay_reconcile_query_failed", "order_id", order.ID, "error", err)
			if auditErr := r.store.RecordAudit(model.SeverityWarn, "system", "epay.reconcile", order.ID,
				"gateway query failed; will retry"); auditErr != nil {
				return auditErr
			}
			continue
		case errors.Is(err, ErrDisabled):
			return ErrDisabled
		default:
			logger.Warn("epay_reconcile_query_failed", "order_id", order.ID, "error", err)
			if auditErr := r.store.RecordAudit(model.SeverityWarn, "system", "epay.reconcile", order.ID,
				"gateway query failed; will retry"); auditErr != nil {
				return auditErr
			}
			continue
		}
		if !status.Paid() {
			continue
		}
		// Epay money has no currency; a non-CNY snapshot must never be
		// settled as if it were CNY.
		if !strings.EqualFold(strings.TrimSpace(order.Plan.Currency), "CNY") {
			if auditErr := r.store.RecordAudit(model.SeverityWarn, "system", "epay.reconcile", order.ID,
				"order currency is not CNY; epay settlement skipped"); auditErr != nil {
				return auditErr
			}
			continue
		}
		if status.OutTradeNo != "" && status.OutTradeNo != order.ID {
			if auditErr := r.store.RecordAudit(model.SeverityWarn, "system", "epay.reconcile", order.ID,
				"gateway returned a different out_trade_no"); auditErr != nil {
				return auditErr
			}
			continue
		}
		amount, err := ParseMoneyMinor(status.Money)
		if err != nil || amount != order.Plan.AmountMinor {
			if auditErr := r.store.RecordAudit(model.SeverityWarn, "system", "epay.reconcile", order.ID,
				"gateway amount does not match the order snapshot"); auditErr != nil {
				return auditErr
			}
			continue
		}
		tradeNo := strings.TrimSpace(status.TradeNo)
		if tradeNo == "" || len(tradeNo) > 128 || strings.ContainsAny(tradeNo, "\r\n\t") {
			if auditErr := r.store.RecordAudit(model.SeverityWarn, "system", "epay.reconcile", order.ID,
				"gateway response has no usable trade_no"); auditErr != nil {
				return auditErr
			}
			continue
		}
		event := model.PaymentEvent{
			Provider:    model.ProviderEpay,
			ID:          tradeNo,
			Kind:        model.PaymentSettled,
			OrderID:     order.ID,
			ProviderRef: tradeNo,
			AmountMinor: order.Plan.AmountMinor,
			Currency:    order.Plan.Currency,
			OccurredAt:  now().UTC().Format(time.RFC3339Nano),
		}
		after, err := r.store.ApplyPaymentEvent(model.ProviderEpay, event, "system")
		if err != nil {
			if auditErr := r.store.RecordAudit(model.SeverityWarn, "system", "epay.reconcile", order.ID,
				"gateway reported TRADE_SUCCESS but the settlement was rejected"); auditErr != nil {
				return auditErr
			}
			continue
		}
		if after.ReviewRequired {
			if auditErr := r.store.RecordAudit(model.SeverityWarn, "system", "epay.reconcile", order.ID,
				"gateway reported TRADE_SUCCESS; settlement requires review"); auditErr != nil {
				return auditErr
			}
			continue
		}
		if after.State == model.OrderPaid {
			// The money is recorded but no entitlement serves yet: an initial
			// fulfilment job owns the remaining work. Reporting "settled" here
			// would claim service the customer does not have.
			if auditErr := r.store.RecordAudit(model.SeverityInfo, "system", "epay.reconcile", order.ID,
				"gateway reported TRADE_SUCCESS; payment recorded, entitlement pending fulfilment"); auditErr != nil {
				return auditErr
			}
			continue
		}
		if auditErr := r.store.RecordAudit(model.SeverityInfo, "system", "epay.reconcile", order.ID,
			"gateway reported TRADE_SUCCESS; settlement applied"); auditErr != nil {
			return auditErr
		}
	}
	return nil
}

// jitteredEpayInterval spreads the poll interval by ±10%, so a fleet of daemons
// does not synchronise on the same second.
func jitteredEpayInterval(base time.Duration) time.Duration {
	if base <= 0 {
		return base
	}
	spread := base / 10
	if spread <= 0 {
		return base
	}
	return base - spread + time.Duration(rand.Int64N(int64(2*spread)+1))
}
