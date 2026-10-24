package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/health"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/renewal"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/usage"
	"ajsaccountd/internal/webhook"
)

// RenewalService is the cached plan catalogue plus the per-account expiry
// derivation. *renewal.Service implements it; a nil Config.Renewal answers 503.
type RenewalService interface {
	Plans(ctx context.Context) (renewal.PlansSnapshot, error)
	Status(accountID, expiresAt string) (renewal.AccountRenewal, error)
}

// HealthMonitor is the loopback gate sampler. *health.Sampler implements it.
// The API watches a node after a successful connect/commit and unwatches it on
// disconnect/delete/logout, so the sampler follows the live gate set.
type HealthMonitor interface {
	Watch(key string, port int) error
	Unwatch(key string)
	Stats(key string) (health.Stats, bool)
}

// WebhookSecretStore persists signing secrets outside the state snapshot.
// The main package backs it with secret.Store; a nil value selects the
// in-memory fallback (secrets do not survive a restart).
type WebhookSecretStore interface {
	Seal(id string, secret []byte) error
	Open(id string) (string, bool)
	Delete(id string) error
}

// EventSink receives customer-visible events. The main package fans one event
// out to every subscribed webhook endpoint; a nil sink drops events.
type EventSink interface {
	Emit(event webhook.EventType, data map[string]any)
}

// handleRenewalPlans serves GET /v1/renewal/plans. A failed refresh is soft:
// the service already falls back to its last good snapshot or the static
// table, and the response reports source and stale so the panel can label it.
func (s *Server) handleRenewalPlans(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Renewal == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"renewal catalogue is not configured")
		return
	}
	snapshot, err := s.cfg.Renewal.Plans(r.Context())
	if err != nil {
		if len(snapshot.Plans) == 0 {
			s.logger.Warn("renewal_plans_unavailable", "error", err)
			writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
				"renewal catalogue is unavailable")
			return
		}
		s.logger.Info("renewal_plans_fallback",
			"source", snapshot.Source, "stale", snapshot.Stale, "error", err)
	}
	writeJSON(w, http.StatusOK, snapshot)
}

// handleRenewalAccount serves GET /v1/renewal/accounts/{id}. The renewal URL is
// always the official purchase page; it carries no account id or credential.
func (s *Server) handleRenewalAccount(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	account, ok := s.store.Account(id)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("account %q not found", id))
		return
	}
	if s.cfg.Renewal == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"renewal catalogue is not configured")
		return
	}
	state, err := s.cfg.Renewal.Status(account.ID, account.ExpiresAt)
	if err != nil {
		// A stored expiry that is not RFC 3339 is a data problem, not a client
		// error; the message carries the parse reason and no credential.
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "account expiry is invalid: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleUsage serves GET /v1/usage. The aggregator owns the window rules: an
// unparsable bound, a reversed window and a window longer than
// usage.MaxWindowDays all answer 400 with the aggregator's reason. A scoped
// key restricted by accountIds/regions aggregates only its accounts' rows.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Usage == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"usage aggregation is not configured")
		return
	}
	query := r.URL.Query()
	from, ok := parseUsageTime(w, query.Get("from"), "from", false)
	if !ok {
		return
	}
	to, ok := parseUsageTime(w, query.Get("to"), "to", true)
	if !ok {
		return
	}
	userID := query.Get("userId")
	if userID == "" {
		userID = query.Get("user_id")
	}
	// The request log stores the account label; accept an account id too so a
	// dashboard link with acc-01 works as expected.
	accountFilter := strings.TrimSpace(query.Get("account"))
	if account, ok := s.accountByIDOrLabel(accountFilter); ok {
		accountFilter = account.Label
	}
	// A restricted key only aggregates rows of the accounts it may read. The
	// auth middleware already refuses an explicit out-of-scope account filter;
	// this narrows the unfiltered case, whose roll-ups would otherwise carry
	// every account.
	rows := s.store.Requests()
	if labels, restricted := s.visibleAccountLabels(r); restricted {
		filtered := make([]model.RequestLog, 0, len(rows))
		for _, row := range rows {
			if _, ok := labels[row.AccountLabel]; ok {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	summary, err := s.cfg.Usage.Aggregate(rows, usage.Query{
		From:    from,
		To:      to,
		Account: accountFilter,
		UserID:  strings.TrimSpace(userID),
		Quotas:  s.usageQuotas(),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
		return
	}
	quotas, enforced := s.usageAccountQuotas(r)
	summary.QuotaEnforced = enforced
	applyAccountQuotas(&summary, quotas)
	writeJSON(w, http.StatusOK, summary)
}

// parseUsageTime accepts RFC 3339 or a bare UTC date. A bare date resolves to
// 00:00:00Z for `from` and to 23:59:59.999999999Z for `to`, so a date-only
// window covers the whole final day instead of stopping at its midnight. Empty
// means "unbounded" and is returned as the zero time, which the aggregator
// defaults.
func parseUsageTime(w http.ResponseWriter, raw, field string, endOfDay bool) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, true
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed, true
	}
	if parsed, err := time.Parse("2006-01-02", raw); err == nil {
		if endOfDay {
			// Inclusive upper bound: the last nanosecond of that UTC day.
			return parsed.UTC().Add(24*time.Hour - time.Nanosecond), true
		}
		return parsed.UTC(), true
	}
	writeError(w, http.StatusBadRequest, CodeInvalidArgument,
		field+" must be an RFC 3339 timestamp or a UTC date (YYYY-MM-DD)")
	return time.Time{}, false
}

// usageQuotas maps user id to monthly quota so the summary can echo it.
func (s *Server) usageQuotas() map[string]*int64 {
	users := s.store.Users()
	quotas := make(map[string]*int64, len(users))
	for _, user := range users {
		if user.QuotaBytesMonth != nil {
			quota := *user.QuotaBytesMonth
			quotas[user.ID] = &quota
		}
	}
	return quotas
}

// accountQuotaView is one account's monthly enforcement figures. budget is nil
// when the account is unlimited.
type accountQuotaView struct {
	budget *int64
	used   int64
}

// usageAccountQuotas returns the request-visible accounts' budgets and current
// UTC month gate usage keyed by account label (the request log's attribution
// unit). enforced reports whether at least one visible account has a budget.
// A scoped key restricted by accountIds/regions sees only its own accounts, so
// a budget outside its scope does not switch its summary to enforced.
func (s *Server) usageAccountQuotas(r *http.Request) (map[string]accountQuotaView, bool) {
	accounts := s.filterAccountsForRequest(r, s.store.Accounts())
	quotas := make(map[string]accountQuotaView, len(accounts))
	enforced := false
	for _, account := range accounts {
		view := accountQuotaView{used: s.store.MonthlyUsage(account.ID)}
		if account.TrafficBudgetBytes != nil {
			budget := *account.TrafficBudgetBytes
			view.budget = &budget
			enforced = true
		}
		quotas[account.Label] = view
	}
	return quotas, enforced
}

// applyAccountQuotas decorates the aggregated account rows with their monthly
// budget, used bytes and clamped remaining bytes. Rows are matched by label,
// the same key the aggregator buckets on.
func applyAccountQuotas(summary *usage.Summary, quotas map[string]accountQuotaView) {
	for i := range summary.Accounts {
		view, ok := quotas[summary.Accounts[i].Account]
		if !ok {
			continue
		}
		summary.Accounts[i].TrafficBudgetBytes = view.budget
		summary.Accounts[i].UsedBytes = view.used
		if view.budget != nil {
			remaining := *view.budget - view.used
			if remaining < 0 {
				remaining = 0
			}
			summary.Accounts[i].RemainingBytes = &remaining
		}
	}
}

type healthNodesResponse struct {
	Enabled bool             `json:"enabled"`
	Reason  string           `json:"reason,omitempty"`
	Nodes   []healthNodeView `json:"nodes"`
}

type healthNodeView struct {
	NodeID              string            `json:"nodeId"`
	AccountID           string            `json:"accountId,omitempty"`
	ServerID            string            `json:"serverId,omitempty"`
	Label               string            `json:"label,omitempty"`
	State               string            `json:"state"`
	Count               int               `json:"count"`
	OK                  int               `json:"ok"`
	SuccessRate         float64           `json:"successRate"`
	P50Ms               float64           `json:"p50Ms"`
	P95Ms               float64           `json:"p95Ms"`
	MinMs               float64           `json:"minMs"`
	MaxMs               float64           `json:"maxMs"`
	ConsecutiveFailures int               `json:"consecutiveFailures"`
	Last                *healthSampleView `json:"last,omitempty"`
}

type healthSampleView struct {
	At         string  `json:"at"`
	LatencyMs  float64 `json:"latencyMs"`
	OK         bool    `json:"ok"`
	ErrorClass string  `json:"errorClass,omitempty"`
}

// handleHealthNodes serves GET /v1/health/nodes: one row per watched node with
// the retained aggregate and the newest sample. Nodes that were never watched
// (or whose ring is between an eviction and the next reconciler heal) are
// omitted, and an account-restricted key only sees nodes of its own accounts.
func (s *Server) handleHealthNodes(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Health == nil {
		writeJSON(w, http.StatusOK, healthNodesResponse{
			Enabled: false,
			Reason:  "AJS_HEALTH_PROBE=0",
			Nodes:   []healthNodeView{},
		})
		return
	}
	nodes := s.store.Nodes()
	visible, restricted := s.visibleAccountIDs(r)
	out := make([]healthNodeView, 0, len(nodes))
	for _, node := range nodes {
		if restricted {
			if _, ok := visible[node.AccountID]; !ok {
				continue
			}
		}
		stats, ok := s.cfg.Health.Stats(node.ID)
		if !ok {
			continue
		}
		view := healthNodeView{
			NodeID:              node.ID,
			AccountID:           node.AccountID,
			ServerID:            node.ServerID,
			Label:               node.Label,
			State:               stats.State,
			Count:               stats.Count,
			OK:                  stats.OK,
			SuccessRate:         stats.SuccessRate,
			P50Ms:               stats.P50Ms,
			P95Ms:               stats.P95Ms,
			MinMs:               stats.MinMs,
			MaxMs:               stats.MaxMs,
			ConsecutiveFailures: stats.ConsecutiveFailures,
		}
		if !stats.Last.At.IsZero() {
			view.Last = &healthSampleView{
				At:         stats.Last.At.UTC().Format(time.RFC3339Nano),
				LatencyMs:  stats.Last.LatencyMs,
				OK:         stats.Last.OK,
				ErrorClass: stats.Last.ErrorClass,
			}
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, healthNodesResponse{Enabled: true, Nodes: out})
}

// watchHealth registers one live gate. Errors are logged: a health probe is a
// diagnostic, never a reason to fail a connect.
func (s *Server) watchHealth(nodeID string, port int) {
	if s.cfg.Health == nil || nodeID == "" || port <= 0 {
		return
	}
	if err := s.cfg.Health.Watch(nodeID, port); err != nil {
		s.logger.Warn("health_watch_failed", "node_id", nodeID, "error", err)
	}
}

// unwatchHealth stops sampling one node. The retained window is kept by the
// sampler, so the last samples stay visible after a disconnect.
func (s *Server) unwatchHealth(nodeID string) {
	if s.cfg.Health == nil || nodeID == "" {
		return
	}
	s.cfg.Health.Unwatch(nodeID)
}

// watchAccountHealth registers every live gate of one account. The login sync
// opens gates outside the connect handler, so it needs the same follow-up.
func (s *Server) watchAccountHealth(accountID string) {
	if s.cfg.Health == nil || accountID == "" {
		return
	}
	for _, node := range s.store.Nodes() {
		if node.AccountID != accountID || node.State != model.NodeLive || node.GatePort <= 0 {
			continue
		}
		s.watchHealth(node.ID, node.GatePort)
	}
}

// unwatchAccountHealth stops sampling every node of one account. It runs after
// logout drained the nodes and after delete removed them.
func (s *Server) unwatchAccountHealth(accountID string) {
	if s.cfg.Health == nil || accountID == "" {
		return
	}
	for _, node := range s.store.Nodes() {
		if node.AccountID == accountID {
			s.unwatchHealth(node.ID)
		}
	}
}

// accountNodeIDs returns the node ids of one account. A delete removes the
// rows, so the caller must capture them before the store mutation.
func (s *Server) accountNodeIDs(accountID string) []string {
	nodes := s.store.Nodes()
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node.AccountID == accountID {
			ids = append(ids, node.ID)
		}
	}
	return ids
}

// observeConnect follows one successful switch: the new gate is sampled, the
// old gate is dropped, and the matching customer event is emitted.
func (s *Server) observeConnect(result runtime.SwitchResult) {
	s.watchHealth(result.NodeID, result.GatePort)
	if result.SwitchedFrom != "" && result.SwitchedFrom != result.ServerID {
		s.unwatchHealth(result.AccountID + ":" + result.SwitchedFrom)
	}
	if result.AlreadyConnected {
		return
	}
	data := map[string]any{
		"accountId":  result.AccountID,
		"serverId":   result.ServerID,
		"nodeId":     result.NodeID,
		"gatePort":   result.GatePort,
		"egressIp":   result.EgressIP,
		"durationMs": result.DurationMs,
	}
	if result.SwitchedFrom != "" && result.SwitchedFrom != result.ServerID {
		data["fromServerId"] = result.SwitchedFrom
		s.emit(webhook.EventNodeSwitched, data)
		return
	}
	s.emit(webhook.EventNodeConnected, data)
}

// observeDisconnect follows one successful disconnect. Disconnect closes every
// gate of the account, so the account's node set is dropped from the sampler;
// the result node is dropped explicitly because a controller test double may
// not have written it to the store.
func (s *Server) observeDisconnect(result runtime.DisconnectResult) {
	if result.NodeID != "" {
		s.unwatchHealth(result.NodeID)
	}
	if result.AccountID != "" {
		s.unwatchAccountHealth(result.AccountID)
	}
	if result.AlreadyDisconnected {
		return
	}
	s.emit(webhook.EventNodeDisconnected, map[string]any{
		"accountId":   result.AccountID,
		"serverId":    result.ServerID,
		"nodeId":      result.NodeID,
		"forced":      result.Forced,
		"activeFlows": result.ActiveFlows,
		"durationMs":  result.DurationMs,
	})
}

// emit hands one event to the configured sink. A nil sink is a no-op so a
// daemon without webhooks pays nothing.
func (s *Server) emit(event webhook.EventType, data map[string]any) {
	if s.cfg.Events == nil {
		return
	}
	s.cfg.Events.Emit(event, data)
}

type webhookEndpointView struct {
	model.WebhookEndpoint
	SecretAvailable bool `json:"secretAvailable"`
}

type webhookCreated struct {
	model.WebhookEndpoint
	SecretAvailable bool   `json:"secretAvailable"`
	Secret          string `json:"secret"`
}

type webhookPatch struct {
	Enabled *bool `json:"enabled"`
}

// handleListWebhooks serves GET /v1/webhooks. The signing secret is never part
// of a read; SecretAvailable reports whether the sealed entry can be opened.
func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	endpoints := s.store.WebhookEndpoints()
	out := make([]webhookEndpointView, 0, len(endpoints))
	for _, endpoint := range endpoints {
		_, available := s.webhookSecret(endpoint.ID)
		out = append(out, webhookEndpointView{WebhookEndpoint: endpoint, SecretAvailable: available})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateWebhook serves POST /v1/webhooks and returns the signing secret
// exactly once. The secret is sealed before the response; a sealing failure
// rolls the endpoint back so the registry never points at a receiver that
// cannot be signed.
func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Webhooks == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"webhook dispatcher is not configured")
		return
	}
	var input model.WebhookEndpointInput
	if !decodeJSON(w, r, &input) {
		return
	}
	secret, err := newWebhookSecret()
	if err != nil {
		s.logger.Error("webhook_secret_generate_failed", "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
		return
	}
	endpoint, err := s.store.CreateWebhookEndpoint(input)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	registered := webhook.Endpoint{
		ID:          endpoint.ID,
		URL:         endpoint.URL,
		Secret:      secret,
		Events:      webhookEvents(endpoint.Events),
		Enabled:     endpoint.Enabled,
		Description: endpoint.Description,
	}
	if parsed, err := time.Parse(time.RFC3339, endpoint.CreatedAt); err == nil {
		registered.CreatedAt = parsed
	}
	if err := s.cfg.Webhooks.AddEndpoint(registered); err != nil {
		_ = s.store.DeleteWebhookEndpoint(endpoint.ID)
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
		return
	}
	if err := s.storeWebhookSecret(endpoint.ID, secret); err != nil {
		s.cfg.Webhooks.RemoveEndpoint(endpoint.ID)
		_ = s.store.DeleteWebhookEndpoint(endpoint.ID)
		s.logger.Error("webhook_secret_seal_failed", "endpoint", endpoint.ID, "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, webhookCreated{
		WebhookEndpoint: endpoint,
		SecretAvailable: true,
		Secret:          secret,
	})
}

// handlePatchWebhook serves PATCH /v1/webhooks/{id}; only `enabled` is
// mutable. Enabling a receiver whose signing secret is unavailable is a 409:
// every delivery is signed, so it could never succeed.
func (s *Server) handlePatchWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Webhooks == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"webhook dispatcher is not configured")
		return
	}
	id := pathParam(r, "id")
	if _, ok := s.store.WebhookEndpoint(id); !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("webhook %q not found", id))
		return
	}
	var patch webhookPatch
	if !decodeJSON(w, r, &patch) {
		return
	}
	if patch.Enabled == nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "enabled is required")
		return
	}
	if *patch.Enabled {
		if _, ok := s.webhookSecret(id); !ok {
			writeError(w, http.StatusConflict, CodeConflict,
				"webhook signing secret is unavailable; recreate the endpoint")
			return
		}
	}
	endpoint, err := s.store.SetWebhookEndpointEnabled(id, *patch.Enabled)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if s.cfg.Webhooks != nil {
		if err := s.cfg.Webhooks.SetEndpointEnabled(id, *patch.Enabled); err != nil &&
			!errors.Is(err, webhook.ErrEndpointMissing) {
			s.logger.Warn("webhook_toggle_failed", "endpoint", id, "error", err)
		}
	}
	_, available := s.webhookSecret(id)
	writeJSON(w, http.StatusOK, webhookEndpointView{WebhookEndpoint: endpoint, SecretAvailable: available})
}

// handleDeleteWebhook serves DELETE /v1/webhooks/{id}. The secret is removed
// with the row; a failed secret delete is logged and does not resurrect the
// endpoint (startup pruning removes the orphan).
func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Webhooks == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUpstreamUnavailable,
			"webhook dispatcher is not configured")
		return
	}
	id := pathParam(r, "id")
	if err := s.store.DeleteWebhookEndpoint(id); err != nil {
		s.writeStoreError(w, err)
		return
	}
	if s.cfg.Webhooks != nil {
		s.cfg.Webhooks.RemoveEndpoint(id)
	}
	if err := s.deleteWebhookSecret(id); err != nil {
		s.logger.Warn("webhook_secret_delete_failed", "endpoint", id, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// Delivery history page bounds. The store retains 50 rows per endpoint and
// the dispatcher ring 200, so a default page of 200 shows the live window and
// a larger limit is only useful after a restart.
const (
	defaultDeliveriesLimit = 200
	maxDeliveriesLimit     = 500
)

// handleWebhookDeliveries serves GET /v1/webhooks/{id}/deliveries. Persisted
// rows from state.json are authoritative; the dispatcher's in-memory ring adds
// attempts the store has not seen yet (or already dropped at its retention
// cap). Both are merged, deduped by delivery id and returned newest first, so a
// restart never hides history and the live view never lags. The route answers
// from the store even when the dispatcher is disabled.
func (s *Server) handleWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if _, ok := s.store.WebhookEndpoint(id); !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("webhook %q not found", id))
		return
	}
	limit, ok := parseDeliveriesLimit(w, r.URL.Query().Get("limit"))
	if !ok {
		return
	}
	deliveries := s.webhookDeliveries(id)
	if len(deliveries) > limit {
		deliveries = deliveries[:limit]
	}
	writeJSON(w, http.StatusOK, deliveries)
}

// webhookDeliveries merges the persisted history with the live ring. Store rows
// come first, so a duplicate id in the ring keeps the persisted copy; the ring
// contributes only attempts missing from the store (younger than the last
// snapshot, or evicted by the per-endpoint cap).
func (s *Server) webhookDeliveries(endpointID string) []webhook.Delivery {
	persisted := s.store.WebhookDeliveries(endpointID)
	out := make([]webhook.Delivery, 0, len(persisted)+webhook.DefaultRingSize)
	seen := make(map[string]struct{}, len(persisted))
	for _, row := range persisted {
		out = append(out, webhookDeliveryFromModel(row))
		seen[row.ID] = struct{}{}
	}
	if s.cfg.Webhooks != nil {
		ring := s.cfg.Webhooks.Log().RecentFor(endpointID) // oldest first
		for i := len(ring) - 1; i >= 0; i-- {
			if _, ok := seen[ring[i].ID]; ok {
				continue
			}
			seen[ring[i].ID] = struct{}{}
			out = append(out, ring[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// webhookDeliveryFromModel converts a persisted row to the wire type. The
// store validates At on write and on restore, so the parse cannot fail for a
// row that reached this point; a zero time would only sort it last.
func webhookDeliveryFromModel(row model.WebhookDelivery) webhook.Delivery {
	at, _ := time.Parse(time.RFC3339, row.At)
	return webhook.Delivery{
		ID:         row.ID,
		EndpointID: row.EndpointID,
		Event:      webhook.EventType(row.Event),
		Attempt:    row.Attempt,
		Status:     webhook.DeliveryStatus(row.Status),
		HTTPStatus: row.HTTPStatus,
		Error:      row.Error,
		At:         at,
		DurationMs: row.DurationMs,
	}
}

// parseDeliveriesLimit reads ?limit. Absent means the default page; a
// non-positive or non-integer value is 400 and a larger one is clamped.
func parseDeliveriesLimit(w http.ResponseWriter, raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultDeliveriesLimit, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "limit must be a positive integer")
		return 0, false
	}
	if limit > maxDeliveriesLimit {
		limit = maxDeliveriesLimit
	}
	return limit, true
}

// webhookEvents converts the persisted event strings to the dispatcher type.
func webhookEvents(events []string) []webhook.EventType {
	out := make([]webhook.EventType, 0, len(events))
	for _, event := range events {
		out = append(out, webhook.EventType(event))
	}
	return out
}

// newWebhookSecret returns 32 random bytes as hex, the same shape as the
// scoped-key secret so operators have one copy/paste convention.
func newWebhookSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// storeWebhookSecret seals one signing secret. Without a secret.Store the
// secret stays in process memory, which keeps tests and a keyless lab run
// working while the README documents that it does not survive a restart.
func (s *Server) storeWebhookSecret(id, secret string) error {
	if s.cfg.WebhookSecrets != nil {
		return s.cfg.WebhookSecrets.Seal(id, []byte(secret))
	}
	s.webhookMu.Lock()
	s.webhookSecrets[id] = secret
	s.webhookMu.Unlock()
	return nil
}

func (s *Server) webhookSecret(id string) (string, bool) {
	if s.cfg.WebhookSecrets != nil {
		return s.cfg.WebhookSecrets.Open(id)
	}
	s.webhookMu.Lock()
	defer s.webhookMu.Unlock()
	secret, ok := s.webhookSecrets[id]
	return secret, ok
}

func (s *Server) deleteWebhookSecret(id string) error {
	if s.cfg.WebhookSecrets != nil {
		return s.cfg.WebhookSecrets.Delete(id)
	}
	s.webhookMu.Lock()
	delete(s.webhookSecrets, id)
	s.webhookMu.Unlock()
	return nil
}
