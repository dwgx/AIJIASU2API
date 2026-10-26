package runtime

import (
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"

	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// acceptedStatus is the request-log status for a flow that reached relay. The
// gate only emits a FlowStatusOK record after the SOCKS5 success reply, so an
// accepted flow is a completed 200 for usage purposes.
const acceptedStatus = 200

// defaultUserFlowCap is the per-user concurrent-flow limit when
// AJS_GATE_USER_FLOWS is unset or invalid. It keeps the admission pre-check
// from being multiplied by many simultaneous long-lived flows.
const defaultUserFlowCap = 8

// FlowMeter turns finished gate flows into request-log rows for the usage
// pipeline. Before this seam only seeded demo traffic and the mutation lab
// wrote store.Requests, so GET /v1/usage could not reflect real proxy traffic.
//
// Metering is observation-only and bounded: the gate hands each stamped
// gate.FlowRecord to OnFlow in the flow's own goroutine (no goroutine per
// flow), and the meter appends one row through store.AppendRequest, which
// applies the same 10,000-row cap as every other writer. Rejected flows never
// reach relay, so they are dropped here; the Lab flow ring still exposes them
// for diagnosis.
//
// A row carries the account label, node label, region, method, listener
// protocol ("socks5" or "http"), host:port target, status, byte total and
// latency. It never carries a URL, header, session id or credential.
// UserID/UserName are the authenticated gate user in userpass mode and stay
// empty in loopback mode, where the account is the attribution unit.
type FlowMeter struct {
	store    *store.Store
	quota    *Quota
	identity *GateIdentity
}

// NewFlowMeter builds a meter over st. The store must not be nil: it is both
// the label source and the request-log sink. The meter also owns the gate's
// monthly account quota policy and the RFC 1929 identity service, so one
// factory wires metering and enforcement.
func NewFlowMeter(st *store.Store) *FlowMeter {
	return &FlowMeter{store: st, quota: NewQuota(st), identity: NewGateIdentity(st)}
}

// userFlowCapFromEnv reads AJS_GATE_USER_FLOWS. Unset, non-numeric and
// non-positive values fall back to defaultUserFlowCap: a typo must not disable
// the cap that bounds concurrent overrun.
func userFlowCapFromEnv() int {
	raw := strings.TrimSpace(os.Getenv("AJS_GATE_USER_FLOWS"))
	if raw == "" {
		return defaultUserFlowCap
	}
	cap, err := strconv.Atoi(raw)
	if err != nil || cap < 1 {
		return defaultUserFlowCap
	}
	return cap
}

// Progress advances the durable monthly counters for one relayed chunk and
// re-checks both monthly budgets. The gate calls it every 256 KiB, so an
// admitted flow cannot relay far past a budget that the pre-check saw as
// available. It returns false when the flow must stop; the gate then closes the
// tunnel and records quota_exceeded.
//
// A durability failure also returns false: the chunk could not be committed, so
// the panel could never bill or admit against it, and the store is already
// poisoned for main to terminate. Continuing the relay would acknowledge
// traffic the journal does not contain.
//
// The cache entries are advanced before either check so a refusal leaves every
// cached total in step with the durable counter. The store is the source of
// truth; this method only saves the reads a populated cache can answer.
func (m *FlowMeter) Progress(userID, accountID string, n int64) bool {
	if m == nil || m.store == nil || n <= 0 {
		return true
	}
	if err := m.store.AddUsage(userID, accountID, n); err != nil {
		return false
	}
	if m.quota != nil && accountID != "" {
		m.quota.advance(accountID, n)
	}
	if m.identity != nil && userID != "" {
		m.identity.AddUsage(userID, n)
	}
	ok := true
	if m.quota != nil && accountID != "" {
		if allowed, _, _ := m.quota.Check(accountID); !allowed {
			ok = false
		}
	}
	if m.identity != nil && userID != "" && m.identity.overQuota(userID) {
		ok = false
	}
	return ok
}

// Quota returns the admission policy the factory attaches to every gate.
func (m *FlowMeter) Quota() *Quota {
	return m.quota
}

// Identity returns the authenticator and per-user policy the node controller
// attaches to every gate in userpass mode. It shares the meter's store, so
// accepted flows advance the same per-user usage cache the policy reads.
func (m *FlowMeter) Identity() *GateIdentity {
	if m == nil {
		return nil
	}
	return m.identity
}

// GateFactory returns a GateFactory that attaches the meter, the account quota
// policy and (in userpass mode) the RFC 1929 identity service to every gate the
// node controller starts. Passing the result as NodeControllerConfig.Gates is
// the whole wiring; a nil factory keeps the default unmetered, unenforced gate.
// An explicitly configured authenticator or policy is never replaced.
func (m *FlowMeter) GateFactory() GateFactory {
	return func(cfg gate.Config, session gate.Session) (Gate, error) {
		nodeID, accountID := cfg.NodeID, cfg.AccountID
		cfg.OnFlow = func(record gate.FlowRecord) {
			if err := m.observe(nodeID, accountID, record); err != nil {
				// The completion row could not be committed: the store is
				// poisoned and main is terminating. The relay already finished,
				// so there is nothing to stop; the failure is logged rather than
				// dropped.
				slog.Error("flow_record_persist_failed",
					"node", nodeID, "account", accountID, "error", err)
			}
		}
		if m.quota != nil {
			cfg.QuotaCheck = m.quota.Check
		}
		cfg.MeterProgress = m.Progress
		if m.identity != nil {
			cfg.UserFlowCheck = m.identity.AcquireFlow
			cfg.UserFlowRelease = m.identity.ReleaseFlow
		}
		if cfg.AuthMode == gate.AuthUserPass && m.identity != nil {
			if cfg.Authenticator == nil {
				cfg.Authenticator = m.identity
			}
			if cfg.UserPolicy == nil {
				cfg.UserPolicy = m.identity
			}
		}
		return gate.NewServer(cfg, session)
	}
}

// observe appends one row for an accepted flow. It runs in the gate's flow
// goroutine after the relay finished, so the work is two short store reads and
// one bounded append. The error is the row's durability failure; the caller
// logs it because the relay itself has already ended.
func (m *FlowMeter) observe(nodeID, accountID string, record gate.FlowRecord) error {
	if m == nil || m.store == nil || record.Status != gate.FlowStatusOK {
		return nil
	}
	node, found := m.store.Node(nodeID)
	accountLabel, nodeLabel, region := "", "", ""
	if found {
		accountLabel = node.AccountLabel
		nodeLabel = node.Label
		region = node.Region
	} else if account, ok := m.store.Account(accountID); ok {
		// The node row is written right after the gate starts; a very early
		// flow can beat it. The account label is still authoritative.
		accountLabel = account.Label
	}
	_, err := m.store.AppendRequest(model.RequestLog{
		// TS stays empty: AppendRequest stamps the completion instant. Using
		// StartedAt would prepend a long flow ahead of rows it started after
		// and break every chronological consumer.
		UserID:       record.UserID,
		UserName:     record.UserName,
		AccountLabel: accountLabel,
		NodeLabel:    nodeLabel,
		Region:       region,
		// The gate's own attribution is authoritative: a reconnect bumps the
		// generation and a stale flow must not claim the new one.
		Slot:       record.Slot,
		Generation: record.Generation,
		Method:     record.Method,
		Protocol:   record.Protocol,
		Target:     flowTarget(record.TargetHost, record.TargetPort),
		Status:     acceptedStatus,
		Bytes:      record.BytesIn + record.BytesOut,
		LatencyMs:  int(record.DurationMs),
		Source:     model.SourceGate,
		// The relay advanced the durable counters every 256 KiB, so this row is
		// a log entry only and must not be counted again.
		MeteredLive: true,
	})
	return err
}

// flowTarget renders a flow destination as host:port. The gate only ever
// parses a SOCKS5 CONNECT destination, so this cannot carry a scheme, path,
// query string or userinfo; the join keeps IPv6 literals bracketed.
func flowTarget(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "unknown"
	}
	if port <= 0 {
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}
