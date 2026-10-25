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
}

// Paid reports whether the gateway says the order is settled.
func (s EpayOrderStatus) Paid() bool {
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
	if payload.code() != 1 {
		return EpayOrderStatus{}, ErrOrderNotFound
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
	Data        *epayQueryData  `json:"data"`
}

type epayQueryData struct {
	TradeNo     string `json:"trade_no"`
	OutTradeNo  string `json:"out_trade_no"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Money       string `json:"money"`
	TradeStatus string `json:"trade_status"`
}

func (r epayQueryResponse) code() int {
	raw := strings.TrimSpace(string(r.Code))
	if raw == "" || raw == "null" {
		return 0
	}
	raw = strings.Trim(raw, `"`)
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}

func (r epayQueryResponse) status() EpayOrderStatus {
	status := EpayOrderStatus{
		TradeNo:     strings.TrimSpace(r.TradeNo),
		OutTradeNo:  strings.TrimSpace(r.OutTradeNo),
		Type:        strings.TrimSpace(r.Type),
		Name:        strings.TrimSpace(r.Name),
		Money:       strings.TrimSpace(r.Money),
		TradeStatus: strings.TrimSpace(r.TradeStatus),
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
	}
	return status
}

// EpayReconcileStore is the store surface the reconciler needs. Keeping it an
// interface makes the polling loop testable without a live gateway or store.
type EpayReconcileStore interface {
	PendingEpayOrders() []model.Order
	ApplyPaymentEvent(provider string, event model.PaymentEvent, actor string) (model.Order, error)
	RecordAudit(severity model.Severity, actor, action, target, detail string)
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

// EpayReconciler polls pending epay orders and settles the ones the gateway
// reports TRADE_SUCCESS. It is intentionally separate from the HTTP surface:
// a restart resumes reconciliation from the persisted order rows.
type EpayReconciler struct {
	provider  *Epay
	store     EpayReconcileStore
	logger    *slog.Logger
	interval  time.Duration
	maxOrders int
	now       func() time.Time
	jitter    func(time.Duration) time.Duration
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
		orders = orders[:maxOrders]
	}
	for _, order := range orders {
		if err := ctx.Err(); err != nil {
			return err
		}
		queryCtx, cancel := context.WithTimeout(ctx, epayQueryTimeout)
		status, err := r.provider.QueryOrder(queryCtx, order.ID)
		cancel()
		switch {
		case err == nil:
		case errors.Is(err, ErrOrderNotFound):
			continue
		case errors.Is(err, ErrDisabled):
			return ErrDisabled
		default:
			logger.Warn("epay_reconcile_query_failed", "order_id", order.ID, "error", err)
			continue
		}
		if !status.Paid() {
			continue
		}
		if status.OutTradeNo != "" && status.OutTradeNo != order.ID {
			r.store.RecordAudit(model.SeverityWarn, "system", "epay.reconcile", order.ID,
				"gateway returned a different out_trade_no")
			continue
		}
		amount, err := ParseMoneyMinor(status.Money)
		if err != nil || amount != order.Plan.AmountMinor {
			r.store.RecordAudit(model.SeverityWarn, "system", "epay.reconcile", order.ID,
				"gateway amount does not match the order snapshot")
			continue
		}
		tradeNo := strings.TrimSpace(status.TradeNo)
		if tradeNo == "" || len(tradeNo) > 128 || strings.ContainsAny(tradeNo, "\r\n\t") {
			r.store.RecordAudit(model.SeverityWarn, "system", "epay.reconcile", order.ID,
				"gateway response has no usable trade_no")
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
			r.store.RecordAudit(model.SeverityWarn, "system", "epay.reconcile", order.ID,
				"gateway reported TRADE_SUCCESS but the settlement was rejected")
			continue
		}
		if after.ReviewRequired {
			r.store.RecordAudit(model.SeverityWarn, "system", "epay.reconcile", order.ID,
				"gateway reported TRADE_SUCCESS; settlement requires review")
			continue
		}
		r.store.RecordAudit(model.SeverityInfo, "system", "epay.reconcile", order.ID,
			"gateway reported TRADE_SUCCESS; settlement applied")
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
