package runtime

import (
	"net"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// acceptedStatus is the request-log status for a flow that reached relay. The
// gate only emits a FlowStatusOK record after the SOCKS5 success reply, so an
// accepted flow is a completed 200 for usage purposes.
const acceptedStatus = 200

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
// A row carries the account label, node label, region, method, host:port
// target, status, byte total and latency. It never carries a URL, header,
// session id or credential. UserID/UserName are the authenticated gate user in
// userpass mode and stay empty in loopback mode, where the account is the
// attribution unit.
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
			m.observe(nodeID, accountID, record)
		}
		if m.quota != nil {
			cfg.QuotaCheck = m.quota.Check
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
// one bounded append.
func (m *FlowMeter) observe(nodeID, accountID string, record gate.FlowRecord) {
	if m == nil || m.store == nil || record.Status != gate.FlowStatusOK {
		return
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
	m.store.AppendRequest(model.RequestLog{
		TS:           record.StartedAt.UTC().Format(time.RFC3339Nano),
		UserID:       record.UserID,
		UserName:     record.UserName,
		AccountLabel: accountLabel,
		NodeLabel:    nodeLabel,
		Region:       region,
		Method:       record.Method,
		Target:       flowTarget(record.TargetHost, record.TargetPort),
		Status:       acceptedStatus,
		Bytes:        record.BytesIn + record.BytesOut,
		LatencyMs:    int(record.DurationMs),
		Source:       model.SourceGate,
	})
	// The next admission must see this row, so the cached monthly totals are
	// advanced (account: dropped and recomputed; user: incremented in place)
	// before the flow goroutine ends.
	m.quota.Invalidate(accountID)
	m.identity.AddUsage(record.UserID, record.BytesIn+record.BytesOut)
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
