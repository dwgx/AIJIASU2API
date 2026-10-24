package model

// Catalog selection and attempt types. These are the persisted, credential-free
// state of the guarded connect path (DESIGN-CATALOG §3.4, §3.8): a selection is
// the operator's desired server, not a running session, and a ServerStat is the
// per-(account, server) outcome counter the catalog row overlays.

// Selection is the persisted single target of one account. It survives
// reloads and is the desired set of the next login sync; it never starts a
// session by itself. UpdatedBy is "operator" for API writes and "system" for a
// selection lost during a login refresh.
type Selection struct {
	AccountID string `json:"accountId"`
	ServerID  string `json:"serverId"`
	UpdatedAt string `json:"updatedAt"`
	UpdatedBy string `json:"updatedBy"`
}

// CatalogSelection is the GET/PUT /v1/accounts/{id}/selection response. Title
// is resolved from the retained login result at read time and is empty when the
// vendor catalog is no longer held (stale).
type CatalogSelection struct {
	AccountID string `json:"accountId"`
	ServerID  string `json:"serverId"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"`
	UpdatedBy string `json:"updatedBy"`
}

// ServerStat is the per-(account, server) connect outcome counter. The store
// key is AccountID + ":" + ServerID. LastLatencyMs is always nil today: no real
// latency probe exists, so the daemon must not invent one.
type ServerStat struct {
	AccountID      string `json:"accountId"`
	ServerID       string `json:"serverId"`
	Attempts       int    `json:"attempts"`
	Successes      int    `json:"successes"`
	LastAttemptAt  string `json:"lastAttemptAt,omitempty"`
	LastSuccessAt  string `json:"lastSuccessAt,omitempty"`
	LastErrorClass string `json:"lastErrorClass,omitempty"`
	LastLatencyMs  *int   `json:"lastLatencyMs,omitempty"`
}
