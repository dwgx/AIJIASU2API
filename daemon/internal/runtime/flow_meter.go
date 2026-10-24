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
// session id or credential, and the SOCKS5 front end has no end-user identity,
// so UserID/UserName stay empty (the account is the attribution unit).
type FlowMeter struct {
	store *store.Store
}

// NewFlowMeter builds a meter over st. The store must not be nil: it is both
// the label source and the request-log sink.
func NewFlowMeter(st *store.Store) *FlowMeter {
	return &FlowMeter{store: st}
}

// GateFactory returns a GateFactory that attaches the meter to every gate the
// node controller starts. Passing the result as NodeControllerConfig.Gates is
// the whole wiring; a nil factory keeps the default unmetered gate.
func (m *FlowMeter) GateFactory() GateFactory {
	return func(cfg gate.Config, session gate.Session) (Gate, error) {
		nodeID, accountID := cfg.NodeID, cfg.AccountID
		cfg.OnFlow = func(record gate.FlowRecord) {
			m.observe(nodeID, accountID, record)
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
