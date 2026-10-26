package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/publisher"
	"ajsaccountd/internal/runtime"
)

type healthResponse struct {
	Status   string         `json:"status"`
	Service  string         `json:"service"`
	Version  string         `json:"version"`
	Time     string         `json:"time"`
	Accounts map[string]int `json:"accounts"`
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	counts := map[string]int{
		string(model.AccountActive):     0,
		string(model.AccountPaused):     0,
		string(model.AccountAuthFailed): 0,
		string(model.AccountExpired):    0,
	}
	for _, account := range s.store.Accounts() {
		counts[string(account.State)]++
	}
	writeJSON(w, http.StatusOK, healthResponse{
		Status:   "ok",
		Service:  "ajs-accountd",
		Version:  s.cfg.Version,
		Time:     time.Now().UTC().Format(time.RFC3339Nano),
		Accounts: counts,
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	window := model.MetricRange(r.URL.Query().Get("range"))
	if window == "" {
		window = model.Range24h
	}
	// A restricted key gets the series for the accounts it may read, built from
	// that explicit snapshot: only visible accountHealth rows contribute.
	accounts := s.filterAccountsForRequest(r, s.store.Accounts())
	metrics, err := s.store.MetricsForAccounts(window, accounts)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

// handlePrometheusMetrics serves GET /metrics in the Prometheus text
// exposition format: aggregate counts only, with every state present including
// zeros. No ids, labels, usernames, IPs or secrets appear in the output.
func (s *Server) handlePrometheusMetrics(w http.ResponseWriter, r *http.Request) {
	// The exposition aggregates accounts, nodes, leases, users, keys, requests
	// and scheduler state globally. None of those counters can be rebuilt from
	// a per-account subset honestly, so a restricted key is refused outright.
	if s.denyGlobalRead(w, r, "global metrics") {
		return
	}
	accounts := map[model.AccountState]int{
		model.AccountActive:     0,
		model.AccountPaused:     0,
		model.AccountAuthFailed: 0,
		model.AccountExpired:    0,
	}
	for _, account := range s.store.Accounts() {
		accounts[account.State]++
	}
	nodes := map[model.NodeState]int{
		model.NodeLive:     0,
		model.NodeReady:    0,
		model.NodeStarting: 0,
		model.NodeDraining: 0,
		model.NodeDown:     0,
		model.NodeStale:    0,
	}
	for _, node := range s.store.Nodes() {
		nodes[node.State]++
	}
	var b strings.Builder
	b.WriteString("# HELP ajs_build_info Build version.\n")
	b.WriteString("# TYPE ajs_build_info gauge\n")
	fmt.Fprintf(&b, "ajs_build_info{version=%q} 1\n", s.cfg.Version)
	b.WriteString("# HELP ajs_uptime_seconds Seconds since process start.\n")
	b.WriteString("# TYPE ajs_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "ajs_uptime_seconds %g\n", time.Since(s.started).Seconds())
	b.WriteString("# HELP ajs_accounts Accounts by state.\n")
	b.WriteString("# TYPE ajs_accounts gauge\n")
	for _, state := range []model.AccountState{model.AccountActive, model.AccountPaused, model.AccountAuthFailed, model.AccountExpired} {
		fmt.Fprintf(&b, "ajs_accounts{state=%q} %d\n", state, accounts[state])
	}
	b.WriteString("# HELP ajs_nodes Nodes by state.\n")
	b.WriteString("# TYPE ajs_nodes gauge\n")
	for _, state := range []model.NodeState{model.NodeLive, model.NodeReady, model.NodeStarting, model.NodeDraining, model.NodeDown, model.NodeStale} {
		fmt.Fprintf(&b, "ajs_nodes{state=%q} %d\n", state, nodes[state])
	}
	b.WriteString("# HELP ajs_leases_total Leases.\n")
	b.WriteString("# TYPE ajs_leases_total gauge\n")
	fmt.Fprintf(&b, "ajs_leases_total %d\n", len(s.store.Leases()))
	b.WriteString("# HELP ajs_users_total Panel users.\n")
	b.WriteString("# TYPE ajs_users_total gauge\n")
	fmt.Fprintf(&b, "ajs_users_total %d\n", len(s.store.Users()))
	b.WriteString("# HELP ajs_api_keys_total API keys.\n")
	b.WriteString("# TYPE ajs_api_keys_total gauge\n")
	fmt.Fprintf(&b, "ajs_api_keys_total %d\n", len(s.store.Keys()))
	b.WriteString("# HELP ajs_requests_total Recorded requests.\n")
	b.WriteString("# TYPE ajs_requests_total gauge\n")
	fmt.Fprintf(&b, "ajs_requests_total %d\n", len(s.store.Requests()))
	if s.cfg.Schedules != nil {
		metrics := s.cfg.Schedules.Metrics()
		b.WriteString("# HELP ajs_schedule_runs_total Schedule runs by result.\n")
		b.WriteString("# TYPE ajs_schedule_runs_total counter\n")
		for _, result := range []string{"ok", "skipped", "error", "dry_run", "unknown"} {
			fmt.Fprintf(&b, "ajs_schedule_runs_total{result=%q} %d\n", result, metrics.Runs[result])
		}
		b.WriteString("# HELP ajs_schedule_fire_unknown_total Fire reservations recovered as unknown.\n")
		b.WriteString("# TYPE ajs_schedule_fire_unknown_total counter\n")
		fmt.Fprintf(&b, "ajs_schedule_fire_unknown_total %d\n", metrics.FireUnknown)
		b.WriteString("# HELP ajs_scheduler_inflight Runs currently executing.\n")
		b.WriteString("# TYPE ajs_scheduler_inflight gauge\n")
		fmt.Fprintf(&b, "ajs_scheduler_inflight %d\n", metrics.Inflight)
		b.WriteString("# HELP ajs_schedule_paused Schedules disabled or paused.\n")
		b.WriteString("# TYPE ajs_schedule_paused gauge\n")
		fmt.Fprintf(&b, "ajs_schedule_paused %d\n", metrics.Paused)
		b.WriteString("# HELP ajs_schedules_total Configured schedules.\n")
		b.WriteString("# TYPE ajs_schedules_total gauge\n")
		fmt.Fprintf(&b, "ajs_schedules_total %d\n", metrics.Schedules)
		b.WriteString("# HELP ajs_schedule_last_run_timestamp_seconds Last run start per schedule.\n")
		b.WriteString("# TYPE ajs_schedule_last_run_timestamp_seconds gauge\n")
		for id, at := range metrics.LastRunUnix {
			fmt.Fprintf(&b, "ajs_schedule_last_run_timestamp_seconds{schedule=%q} %d\n", id, at)
		}
		b.WriteString("# HELP ajs_schedule_next_run_timestamp_seconds Next run per schedule.\n")
		b.WriteString("# TYPE ajs_schedule_next_run_timestamp_seconds gauge\n")
		for id, at := range metrics.NextRunUnix {
			fmt.Fprintf(&b, "ajs_schedule_next_run_timestamp_seconds{schedule=%q} %d\n", id, at)
		}
		b.WriteString("# HELP ajs_account_circuit_open Account switch circuit breaker state.\n")
		b.WriteString("# TYPE ajs_account_circuit_open gauge\n")
		for account, open := range metrics.CircuitOpen {
			if open {
				fmt.Fprintf(&b, "ajs_account_circuit_open{account=%q} 1\n", account)
			}
		}
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(b.String()))
}

// defaultRequestsPageSize bounds GET /v1/requests when the caller omits
// ?limit. The request log is the largest list in the API (149.8 kB for the 40
// seeded rows, BENCH.md §6.5) because every row is a full record, so an
// unbounded default is the one outlier. An explicit ?limit= still wins.
const defaultRequestsPageSize = 200

func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) {
	// A request row stores the account label it was written with, not the
	// account id, and labels are recyclable: after B releases a label and A
	// takes it, filtering A's rows by its current label would return B's
	// history. There is no stable attribution in the log, so a restricted key
	// is refused instead of served a guess. The unrestricted read is unchanged.
	if s.denyGlobalRead(w, r, "the request log") {
		return
	}
	requests := s.store.Requests()
	start, end, ok := paginateWithLimit(w, r, len(requests), defaultRequestsPageSize)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, requests[start:end])
}

func (s *Server) handleCreateMutation(w http.ResponseWriter, r *http.Request) {
	var input model.MutationLabInput
	if !decodeJSON(w, r, &input) {
		return
	}
	entry, err := s.store.CreateMutation(input, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	// Audit rows are global and carry free text, not a reliable account id, so
	// a restricted key is refused before the store is read. Inferring ownership
	// from the detail or target would be a guess presented as a filter.
	if s.denyGlobalRead(w, r, "the audit log") {
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "limit must be a non-negative integer")
			return
		}
		limit = parsed
	}
	if limit > 500 {
		limit = 500
	}
	writeJSON(w, http.StatusOK, s.store.Audit(limit))
}

// settingsView is the GET and PATCH /v1/settings payload: the persisted panel
// settings plus the read-only payment adapter state. The payment fields are
// derived from the live adapter on every read and never stored, so they cannot
// drift from what the checkout and notify routes actually serve.
type settingsView struct {
	model.PanelSettings
	// EpayEnabled is true only when the 易支付 adapter is configured and
	// serving checkout; a nil or incomplete adapter reports false.
	EpayEnabled bool `json:"epayEnabled"`
	// EpayMode names the adapter's settlement shape; empty while disabled.
	EpayMode string `json:"epayMode,omitempty"`
}

// settingsView decorates the stored settings with the live adapter state.
func (s *Server) settingsView(settings model.PanelSettings) settingsView {
	view := settingsView{PanelSettings: settings}
	if s.cfg.Epay != nil && s.cfg.Epay.Configured() && s.cfg.Epay.Enabled() {
		view.EpayEnabled = true
		view.EpayMode = s.cfg.Epay.Mode()
	}
	return view
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.settingsView(s.store.Settings()))
}

func (s *Server) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if !decodeJSON(w, r, &raw) {
		return
	}
	// epayEnabled and epayMode are read-only fields derived from the payment
	// adapter. They are accepted so the dashboard can PATCH back the object it
	// read, and ignored: settingsView recomputes them from the adapter.
	allowed := map[string]struct{}{
		"transport":              {},
		"healthIntervalSec":      {},
		"leaseTtl":               {},
		"subscriptionRefreshSec": {},
		"theme":                  {},
		"epayEnabled":            {},
		"epayMode":               {},
	}
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "unknown settings field "+strconv.Quote(key))
			return
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "invalid settings body")
		return
	}
	var patch model.SettingsPatch
	if err := json.Unmarshal(encoded, &patch); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "invalid settings value: "+err.Error())
		return
	}
	settings, err := s.store.UpdateSettings(patch, s.auditActor(r))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsView(settings))
}

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	topology := s.store.Topology()
	// A restricted key gets the same map built from its visible nodes only:
	// another tenant's node ids and tags never appear, and the map describes
	// exactly the subset the key may read.
	if visible, restricted := s.visibleAccountIDs(r); restricted {
		nodes := make(map[string]string, len(visible))
		for _, node := range s.store.Nodes() {
			if _, ok := visible[node.AccountID]; ok {
				nodes[node.ID] = node.Label
			}
		}
		topology.Nodes = nodes
	}
	writeJSON(w, http.StatusOK, topology)
}

// projectPanelTopology narrows the panel's last-good view to the live nodes of
// the accounts the caller may read. Both the node index and the subscription
// content are global in the panel view, so a restricted key gets them rebuilt
// from the identical visible subset instead of a filtered copy that would still
// carry another tenant's tags in Content.
func (s *Server) projectPanelTopology(r *http.Request, topology runtime.Topology) (runtime.Topology, error) {
	visible, restricted := s.visibleAccountIDs(r)
	if !restricted {
		return topology, nil
	}
	nodes := make(map[string]string, len(visible))
	live := make([]publisher.Node, 0, len(visible))
	for _, node := range s.store.Nodes() {
		if node.State != model.NodeLive {
			continue
		}
		if _, ok := visible[node.AccountID]; !ok {
			continue
		}
		nodes[node.ID] = node.Label
		live = append(live, publisher.Node{Tag: node.Label, GatePort: node.GatePort})
	}
	content, err := publisher.Render(live)
	if err != nil {
		return runtime.Topology{}, err
	}
	topology.Nodes = nodes
	topology.Content = content
	return topology, nil
}
