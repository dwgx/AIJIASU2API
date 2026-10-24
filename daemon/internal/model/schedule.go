package model

import "encoding/json"

// ScheduleAction is the closed set of operations the scheduler can run.
// P0 executes account.rotate, account.reconnect and the system-only renewal
// sweep; the remaining constants exist so a snapshot written by a later phase
// still validates and so the API can name an unsupported action clearly.
type ScheduleAction string

const (
	ActionAccountSwitch       ScheduleAction = "account.switch"
	ActionAccountRotate       ScheduleAction = "account.rotate"
	ActionAccountReconnect    ScheduleAction = "account.reconnect"
	ActionAccountLogin        ScheduleAction = "account.login"
	ActionAccountLogout       ScheduleAction = "account.logout"
	ActionNodeDrain           ScheduleAction = "node.drain"
	ActionNodeUndrain         ScheduleAction = "node.undrain"
	ActionLeaseRotate         ScheduleAction = "lease.rotate"
	ActionSubscriptionRefresh ScheduleAction = "subscription.refresh"
	ActionEgressCheck         ScheduleAction = "egress.check"
	ActionRenewalReminder     ScheduleAction = "account.renewal_reminder"
)

// ScheduleTarget identifies what a schedule acts on.
type ScheduleTarget struct {
	Kind   string          `json:"kind"` // account|node|lease|fleet
	ID     string          `json:"id,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

// ScheduleTrigger selects when a schedule runs.
type ScheduleTrigger struct {
	Kind      string `json:"kind"`                // cron|interval|once
	Expr      string `json:"expr,omitempty"`      // 5-field cron
	EverySec  int    `json:"everySec,omitempty"`  // 60..2592000
	JitterSec int    `json:"jitterSec,omitempty"` // interval/cron jitter
	At        string `json:"at,omitempty"`        // RFC3339, once only
	Timezone  string `json:"timezone,omitempty"`  // IANA; empty = UTC
}

// ScheduleWindow restricts runs to weekdays and a clock range. End < start
// crosses midnight and belongs to the start day.
type ScheduleWindow struct {
	Days  []int  `json:"days"`  // 0=Sunday..6=Saturday; empty = all
	Start string `json:"start"` // "HH:MM" inclusive
	End   string `json:"end"`   // "HH:MM" exclusive
}

// ScheduleGuard bounds how often a schedule may run.
type ScheduleGuard struct {
	CooldownSec   int  `json:"cooldownSec"`
	MaxRunsPerDay int  `json:"maxRunsPerDay"`
	DryRun        bool `json:"dryRun"`
}

// Schedule is the durable schedule resource. Server-managed fields are never
// written by a client; the API rejects them on create and PATCH.
type Schedule struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Owner    string `json:"owner"`

	Action  ScheduleAction  `json:"action"`
	Target  ScheduleTarget  `json:"target"`
	Trigger ScheduleTrigger `json:"trigger"`
	Window  *ScheduleWindow `json:"window,omitempty"`
	Guard   ScheduleGuard   `json:"guard"`

	MisfirePolicy     string `json:"misfirePolicy,omitempty"`     // skip|run_once
	MisfireGraceSec   int    `json:"misfireGraceSec,omitempty"`   // 0..3600
	ConcurrencyPolicy string `json:"concurrencyPolicy,omitempty"` // forbid|queue

	CreatedAt           string `json:"createdAt"`
	UpdatedAt           string `json:"updatedAt"`
	LastRunAt           string `json:"lastRunAt,omitempty"`
	LastScheduledAt     string `json:"lastScheduledAt,omitempty"`
	LastResult          string `json:"lastResult,omitempty"` // ok|skipped|error|dry_run|unknown
	LastError           string `json:"lastError,omitempty"`
	NextRunAt           string `json:"nextRunAt,omitempty"`
	RunCountToday       int    `json:"runCountToday"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	PausedReason        string `json:"pausedReason,omitempty"`
	Degraded            bool   `json:"degraded"`
	Cursor              int    `json:"cursor,omitempty"`
}

// ScheduleRun is one execution attempt. Detail is a short label, never a
// credential or an upstream body.
type ScheduleRun struct {
	ID           string `json:"id"`
	ScheduleID   string `json:"scheduleId"`
	FireKey      string `json:"fireKey,omitempty"`
	Trigger      string `json:"trigger"` // cron|interval|once|manual
	ScheduledFor string `json:"scheduledFor,omitempty"`
	StartedAt    string `json:"startedAt"`
	FinishedAt   string `json:"finishedAt,omitempty"`
	State        string `json:"state"`            // running|succeeded|failed|skipped|dry_run|unknown
	Result       string `json:"result,omitempty"` // ok|skipped|error|dry_run|unknown
	Reason       string `json:"reason,omitempty"`
	ErrorClass   string `json:"errorClass,omitempty"`
	Detail       string `json:"detail,omitempty"`
	AuditID      string `json:"auditId,omitempty"`
	OperationID  string `json:"operationId,omitempty"`
	Actor        string `json:"actor,omitempty"`
}

// ScheduleFire is one durable fire-key reservation.
type ScheduleFire struct {
	Key           string `json:"key"`
	ScheduleID    string `json:"scheduleId"`
	ScheduledUnix int64  `json:"scheduledUnix"`
	State         string `json:"state"` // reserved|succeeded|failed|skipped|dry_run|unknown
	RunID         string `json:"runId,omitempty"`
	Owner         string `json:"owner,omitempty"`
	ReservedAt    string `json:"reservedAt"`
	FinishedAt    string `json:"finishedAt,omitempty"`
}

// ReminderMark records one renewal notice that was already emitted.
type ReminderMark struct {
	AccountID string `json:"accountId"`
	Kind      string `json:"kind"`      // threshold|overdue
	Threshold int    `json:"threshold"` // days; 0 for overdue
	ExpiresAt string `json:"expiresAt"`
	State     string `json:"state"` // sent|dismissed
	MarkedAt  string `json:"markedAt"`
}

// RotateParams is the strict account.rotate parameter body.
type RotateParams struct {
	Strategy       string   `json:"strategy"`
	ServerIDs      []string `json:"serverIds,omitempty"`
	Region         string   `json:"region,omitempty"`
	ExcludeCurrent *bool    `json:"excludeCurrent,omitempty"`
	DrainSeconds   int      `json:"drainSeconds,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
	Rollback       *bool    `json:"rollback,omitempty"`
}

// ReconnectParams is the strict account.reconnect parameter body.
type ReconnectParams struct {
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// ScheduleGuardInput is the partial guard body. Pointer fields distinguish
// "omitted" from an explicit zero/false, so a guard that only sets a cooldown
// does not silently arm the schedule.
type ScheduleGuardInput struct {
	CooldownSec   *int  `json:"cooldownSec,omitempty"`
	MaxRunsPerDay *int  `json:"maxRunsPerDay,omitempty"`
	DryRun        *bool `json:"dryRun,omitempty"`
}

// ScheduleCreateInput is the POST /v1/schedules body. Confirm is a
// deliberate-arm token, not a credential.
type ScheduleCreateInput struct {
	Name              string              `json:"name"`
	Enabled           *bool               `json:"enabled,omitempty"`
	Action            ScheduleAction      `json:"action"`
	Target            *ScheduleTarget     `json:"target,omitempty"`
	Trigger           *ScheduleTrigger    `json:"trigger,omitempty"`
	Window            *ScheduleWindow     `json:"window,omitempty"`
	Guard             *ScheduleGuardInput `json:"guard,omitempty"`
	MisfirePolicy     string              `json:"misfirePolicy,omitempty"`
	MisfireGraceSec   *int                `json:"misfireGraceSec,omitempty"`
	ConcurrencyPolicy string              `json:"concurrencyPolicy,omitempty"`
	Confirm           string              `json:"confirm,omitempty"`
}

// SchedulePatchInput is the PATCH /v1/schedules/{id} body. Nil fields are
// unchanged; server-managed fields are not part of the schema.
type SchedulePatchInput struct {
	Name              *string             `json:"name,omitempty"`
	Enabled           *bool               `json:"enabled,omitempty"`
	Action            *ScheduleAction     `json:"action,omitempty"`
	Target            *ScheduleTarget     `json:"target,omitempty"`
	Trigger           *ScheduleTrigger    `json:"trigger,omitempty"`
	Window            *ScheduleWindow     `json:"window,omitempty"`
	Guard             *ScheduleGuardInput `json:"guard,omitempty"`
	MisfirePolicy     *string             `json:"misfirePolicy,omitempty"`
	MisfireGraceSec   *int                `json:"misfireGraceSec,omitempty"`
	ConcurrencyPolicy *string             `json:"concurrencyPolicy,omitempty"`
	Confirm           string              `json:"confirm,omitempty"`
}

// ScheduleRunInput is the POST /v1/schedules/{id}/actions/run body.
type ScheduleRunInput struct {
	Confirm string `json:"confirm,omitempty"`
	DryRun  *bool  `json:"dryRun,omitempty"`
	Force   bool   `json:"force,omitempty"`
}

// SchedulePreviewInput is the POST /v1/schedules/preview body.
type SchedulePreviewInput struct {
	Action  ScheduleAction   `json:"action,omitempty"`
	Target  *ScheduleTarget  `json:"target,omitempty"`
	Trigger *ScheduleTrigger `json:"trigger"`
	Window  *ScheduleWindow  `json:"window,omitempty"`
}

// SchedulePreviewRun is one computed future occurrence.
type SchedulePreviewRun struct {
	At       string `json:"at"`
	UTC      string `json:"utc"`
	InWindow bool   `json:"inWindow"`
}

// SchedulePreview is the pure preview response.
type SchedulePreview struct {
	ServerTime string               `json:"serverTime"`
	Runs       []SchedulePreviewRun `json:"runs"`
	Warnings   []string             `json:"warnings"`
}
