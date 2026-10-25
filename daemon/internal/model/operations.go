package model

// OperationState is the lifecycle state of one async operation
// (DESIGN-INTEGRATION §3.4): queued → running → succeeded|failed|canceled.
// Terminal records are persisted so a script can poll across a daemon
// restart; a record that was still queued or running when the daemon stopped
// is restored as failed with code OPERATION_UNKNOWN.
type OperationState string

const (
	OperationQueued    OperationState = "queued"
	OperationRunning   OperationState = "running"
	OperationSucceeded OperationState = "succeeded"
	OperationFailed    OperationState = "failed"
	OperationCanceled  OperationState = "canceled"
)

// Valid reports whether the state belongs to the closed vocabulary.
func (s OperationState) Valid() bool {
	switch s {
	case OperationQueued, OperationRunning, OperationSucceeded, OperationFailed, OperationCanceled:
		return true
	default:
		return false
	}
}

// Terminal reports whether no further transition can happen. Only terminal
// records are retained beyond the in-flight set and survive a restart.
func (s OperationState) Terminal() bool {
	switch s {
	case OperationSucceeded, OperationFailed, OperationCanceled:
		return true
	default:
		return false
	}
}

// Operation type names. They name the guarded switch the executor used, not a
// second switch implementation: both run through CatalogConnector.ConnectServer.
const (
	OperationTypeSwitch  = "account.switch"
	OperationTypeConnect = "account.connect"
)

// Operation step states. A step is one observable boundary of the executor;
// the runtime keeps its own detailed audit rows for drain/close/open/gate.
const (
	OperationStepOK       = "ok"
	OperationStepFailed   = "failed"
	OperationStepCanceled = "canceled"
)

// OperationTarget is the resolved selection target. At most one of the
// request forms is stored; ServerID is always the resolved vendor server.
type OperationTarget struct {
	ServerID string `json:"serverId,omitempty"`
	NodeID   string `json:"nodeId,omitempty"`
	Region   string `json:"region,omitempty"`
	Strategy string `json:"strategy,omitempty"`
}

// OperationStep is one timed executor boundary. Name is a stable label
// ("validate", "switch", "interrupted"), never a vendor response.
type OperationStep struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	DurationMs int64  `json:"durationMs"`
}

// OperationError is the client-safe failure of one operation. Code is the
// stable machine-readable class; the wrapped cause stays in the log.
type OperationError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

// SelectNodeResult is the terminal result of a select-node operation and the
// body of the idempotent 200 no-op. Changed is false only when the requested
// server was already the live one.
type SelectNodeResult struct {
	Changed           bool     `json:"changed"`
	AccountID         string   `json:"accountId"`
	NodeID            string   `json:"nodeId,omitempty"`
	ServerID          string   `json:"serverId"`
	GatePort          int      `json:"gatePort,omitempty"`
	EgressIP          string   `json:"egressIp,omitempty"`
	SessionHealthy    bool     `json:"sessionHealthy"`
	PreviousNodeID    string   `json:"previousNodeId,omitempty"`
	RollbackAttempted bool     `json:"rollbackAttempted,omitempty"`
	Rollback          string   `json:"rollback,omitempty"`
	DurationMs        int64    `json:"durationMs,omitempty"`
	Warnings          []string `json:"warnings,omitempty"`
}

// Operation is one durable async transition. Steps and Result are nil/empty
// until the executor reaches them; StartedAt and FinishedAt are empty until
// the matching transition, so a queued record is never presented as started.
type Operation struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	State       OperationState    `json:"state"`
	AccountID   string            `json:"accountId"`
	Target      OperationTarget   `json:"target"`
	RequestedBy string            `json:"requestedBy"`
	CreatedAt   string            `json:"createdAt"`
	StartedAt   string            `json:"startedAt"`
	FinishedAt  string            `json:"finishedAt"`
	Steps       []OperationStep   `json:"steps"`
	Result      *SelectNodeResult `json:"result"`
	Error       *OperationError   `json:"error"`
}

// SelectNodeRequest is the POST
// /v1/accounts/{id}/actions/select-node body. DrainSeconds and Rollback are
// accepted for the DESIGN-INTEGRATION contract but are advisory: the current
// guarded switch fixes its drain grace and performs metadata-only containment,
// so the executor never invents a parallel switch path.
type SelectNodeRequest struct {
	Target         *OperationTarget `json:"target,omitempty"`
	DrainSeconds   int              `json:"drainSeconds,omitempty"`
	TimeoutSeconds int              `json:"timeoutSeconds,omitempty"`
	IfCurrent      string           `json:"ifCurrent,omitempty"`
	Rollback       *bool            `json:"rollback,omitempty"`
	Wait           bool             `json:"wait,omitempty"`
}
