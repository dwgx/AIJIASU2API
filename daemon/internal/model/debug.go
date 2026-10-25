package model

// Debug trace kinds. A kind names the runtime boundary that emitted the entry,
// not a vendor response. They are the stable filter vocabulary of
// GET /v1/debug/trace and the SSE event name of GET /v1/debug/stream.
const (
	DebugKindSwitchStep  = "switch.step"
	DebugKindSwitch      = "switch"
	DebugKindGateOpen    = "gate.open"
	DebugKindGateClose   = "gate.close"
	DebugKindSessionOpen = "session.open"
	DebugKindSessionLost = "session.lost"
	DebugKindLeaseCreate = "lease.create"
	DebugKindLeaseRevoke = "lease.revoke"
	DebugKindSlotState   = "slot.state"
)

// Debug trace states. They describe the outcome of one boundary and reuse the
// operation-step vocabulary so a client can render both the same way.
const (
	DebugStateOK       = "ok"
	DebugStateFailed   = "failed"
	DebugStateLost     = "lost"
	DebugStateOpen     = "open"
	DebugStateClosed   = "closed"
	DebugStateCanceled = "canceled"
)

// DebugTrace is one redacted runtime event. It is deliberately an allowlist:
// there is no field for a SID, cookie, token, password, device identity or
// arbitrary attribute bag, so a secret cannot be serialized even by mistake.
// Detail is free text and is scrubbed before it enters the ring.
//
// Seq is the ring's monotonic sequence. It is stable for the life of the
// process and increases by one per recorded entry; it is not a durable id and
// restarts at 1 with the process. Slot, Generation and NodeID attribute the
// event to the exact service slot, session generation and node it belongs to;
// zero/empty means the event has no such attribution (for example a lease that
// predates multi-slot).
type DebugTrace struct {
	Seq        uint64 `json:"seq"`
	At         string `json:"at"`
	Kind       string `json:"kind"`
	AccountID  string `json:"accountId,omitempty"`
	NodeID     string `json:"nodeId,omitempty"`
	ServerID   string `json:"serverId,omitempty"`
	Slot       int    `json:"slot,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
	Step       string `json:"step,omitempty"`
	State      string `json:"state,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Detail     string `json:"detail,omitempty"`
	ErrorClass string `json:"errorClass,omitempty"`
}

// DebugTracePage is the GET /v1/debug/trace body. Entries is never null, so a
// client can iterate without a nil check. Capacity and Retained describe the
// bounded in-memory ring; Evicted counts entries overwritten since process
// start. NextSeq is the cursor to pass to the stream (or to a later poll) and
// OldestSeq is the oldest retained sequence.
type DebugTracePage struct {
	Enabled   bool         `json:"enabled"`
	Reason    string       `json:"reason,omitempty"`
	Capacity  int          `json:"capacity"`
	Retained  int          `json:"retained"`
	Evicted   uint64       `json:"evicted"`
	OldestSeq uint64       `json:"oldestSeq,omitempty"`
	NextSeq   uint64       `json:"nextSeq"`
	Entries   []DebugTrace `json:"entries"`
}
