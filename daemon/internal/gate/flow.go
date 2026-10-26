package gate

import (
	"strconv"
	"sync"
	"time"
)

// FlowRingCapacity is the number of finished flows one gate retains. Older
// records are evicted first and counted by Dropped.
const FlowRingCapacity = 500

// Flow statuses. A flow that reached relay is "ok"; every pre-relay failure,
// including a draining or admission refusal, is "rejected".
const (
	FlowStatusOK       = "ok"
	FlowStatusRejected = "rejected"
)

// Flow error classes. They name the rejection seam, never a credential or a
// vendor payload.
const (
	FlowErrorHandshake        = "handshake"
	FlowErrorClosed           = "closed"
	FlowErrorDraining         = "draining"
	FlowErrorAdmissionTimeout = "admission_timeout"
	FlowErrorQuota            = "quota_exceeded"
	FlowErrorOpenFlow         = "open_flow"
	FlowErrorReply            = "reply"

	// Per-user identity classes (RFC 1929). auth_method and auth_failed are
	// the only two a client can distinguish on the wire; every credential
	// rejection shares auth_failed.
	FlowErrorNonLoopbackPeer   = "non_loopback_peer"
	FlowErrorAuthMethod        = "auth_method"
	FlowErrorAuthFailed        = "auth_failed"
	FlowErrorAuthUnavailable   = "auth_unavailable"
	FlowErrorPolicyUnavailable = "policy_unavailable"
	FlowErrorUserDisabled      = "user_disabled"
	FlowErrorAllowlist         = "allowlist"
	FlowErrorUserQuota         = "user_quota_exceeded"
	// FlowErrorLease is the refusal for a flow whose bound lease stopped being
	// the one active lease for (user, gate account): revoked, expired, moved or
	// never resolved. The credential was valid when the handshake completed, so
	// the class names the entitlement, not the key.
	FlowErrorLease = "lease_inactive"
)

// Flow protocols name the listener that admitted a flow. A gate can serve the
// SOCKS5 and the HTTP CONNECT listener side by side; the record says which one.
const (
	FlowProtocolSOCKS5 = "socks5"
	FlowProtocolHTTP   = "http"
)

// FlowRecord is one finished gate flow. It deliberately carries the target as
// host plus port only: never a full URL, headers, a session id or a
// credential. NodeID and AccountID are filled by the runtime that owns the
// gate; a standalone gate leaves them empty.
type FlowRecord struct {
	ID         string    `json:"id"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMs int64     `json:"durationMs"`
	NodeID     string    `json:"nodeId,omitempty"`
	AccountID  string    `json:"accountId,omitempty"`
	// Slot and Generation identify the account slot and session generation
	// that served the flow. They are attribution only and never a credential.
	Slot       int    `json:"slot,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
	// UserID and UserName attribute an authenticated flow to a panel user.
	// They are empty in loopback mode and are attribution only: no key id,
	// key prefix, username/password frame or authenticator input ever
	// reaches a record.
	UserID     string `json:"userId,omitempty"`
	UserName   string `json:"userName,omitempty"`
	Method     string `json:"method"`
	Protocol   string `json:"protocol,omitempty"`
	TargetHost string `json:"targetHost"`
	TargetPort int    `json:"targetPort"`
	Status     string `json:"status"`
	BytesIn    int64  `json:"bytesIn"`
	BytesOut   int64  `json:"bytesOut"`
	ErrorClass string `json:"errorClass,omitempty"`
}

// flowRing is a fixed-capacity, concurrency-safe ring of FlowRecords. The
// oldest record is overwritten once the ring is full; dropped counts those
// evictions since process start.
type flowRing struct {
	mu       sync.Mutex
	capacity int
	buf      []FlowRecord
	start    int
	dropped  uint64
}

func newFlowRing(capacity int) *flowRing {
	if capacity <= 0 {
		capacity = FlowRingCapacity
	}
	return &flowRing{capacity: capacity, buf: make([]FlowRecord, 0, capacity)}
}

func (r *flowRing) add(record FlowRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) < r.capacity {
		r.buf = append(r.buf, record)
		return
	}
	r.buf[r.start] = record
	r.start = (r.start + 1) % r.capacity
	r.dropped++
}

// snapshot returns the newest records first. A limit <= 0 returns the whole
// ring; a limit larger than the ring is clamped by the ring's size.
func (r *flowRing) snapshot(limit int) []FlowRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.buf)
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]FlowRecord, 0, n)
	for i := 1; i <= n; i++ {
		index := (r.start + len(r.buf) - i) % len(r.buf)
		out = append(out, r.buf[index])
	}
	return out
}

func (r *flowRing) droppedCount() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// Flows returns up to limit finished flows, newest first. A limit <= 0
// returns the whole ring.
func (s *Server) Flows(limit int) []FlowRecord {
	return s.flowRing.snapshot(limit)
}

// Dropped returns how many flow records have been evicted from the bounded
// ring since the server was built.
func (s *Server) Dropped() uint64 {
	return s.flowRing.droppedCount()
}

// nextFlowID returns a process-local, node-prefixed identifier. The sequence
// makes ids unique even when several gates record in the same millisecond.
func (s *Server) nextFlowID() string {
	seq := strconv.FormatUint(s.flowSeq.Add(1), 10)
	if s.cfg.NodeID == "" {
		return "flow-" + seq
	}
	return s.cfg.NodeID + "#" + seq
}

func (s *Server) durationMs(started time.Time) int64 {
	duration := s.cfg.Now().Sub(started).Milliseconds()
	if duration < 0 {
		return 0
	}
	return duration
}

// recordFlow stamps the id and duration, then appends to the bounded ring and
// hands the stamped record to the optional observer. The observer runs in the
// flow's goroutine after the ring write, so the Lab view never depends on it.
func (s *Server) recordFlow(record FlowRecord) {
	record.ID = s.nextFlowID()
	record.DurationMs = s.durationMs(record.StartedAt)
	// The gate is the authority for which slot/generation served the flow, so
	// a record cannot disagree with the listener that admitted it.
	record.Slot = s.cfg.Slot
	record.Generation = s.cfg.Generation
	s.flowRing.add(record)
	if s.cfg.OnFlow != nil {
		s.cfg.OnFlow(record)
	}
}

// recordReject records a pre-relay refusal from the SOCKS5 listener. An empty
// target host means the handshake never produced a destination; Method stays
// empty in that case rather than inventing one. principal carries attribution
// only after a successful authentication and stays empty for a failed
// handshake. The HTTP CONNECT listener has its own recorder in http.go.
func (s *Server) recordReject(started time.Time, target target, principal Principal, errorClass string) {
	method := ""
	if target.host != "" {
		method = "CONNECT"
	}
	s.recordFlow(FlowRecord{
		StartedAt:  started,
		UserID:     principal.UserID,
		UserName:   principal.UserName,
		Method:     method,
		Protocol:   FlowProtocolSOCKS5,
		TargetHost: target.host,
		TargetPort: target.port,
		Status:     FlowStatusRejected,
		ErrorClass: errorClass,
	})
}
