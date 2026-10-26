package model

// LeaseRoute is the immutable ingress route a lease credential was issued for.
// The router resolves the gate port from this snapshot instead of accepting a
// tenant-selected node or account, and rechecks it against the live lease on
// every authentication. Generation is zero for a legacy (slot-less) lease.
type LeaseRoute struct {
	NodeID     string `json:"nodeId"`
	AccountID  string `json:"accountId"`
	GatePort   int    `json:"gatePort"`
	Slot       int    `json:"slot,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
}

// LeaseCredential is the persisted metadata of one lease-scoped ingress
// credential. SecretHash is hex(HMAC-SHA256(pepper, secret)): the plaintext
// secret exists only in the Issue/Rotate return value and is never stored,
// logged or snapshotted. Prefix is the short non-secret display prefix.
//
// This type is the store's record shape. An HTTP handler must never serialise
// it directly: the public credential view omits SecretHash and Route.
type LeaseCredential struct {
	ID          string     `json:"id"`
	LeaseID     string     `json:"leaseId"`
	UserID      string     `json:"userId"`
	SecretHash  string     `json:"secretHash"`
	Prefix      string     `json:"prefix"`
	CreatedAt   string     `json:"createdAt"`
	RevokedAt   *string    `json:"revokedAt"`
	SourceCIDRs []string   `json:"sourceCidrs,omitempty"`
	MaxConns    int        `json:"maxConns"`
	Route       LeaseRoute `json:"route"`
}

// IssueLeaseCredentialInput is the store-level issue request. SourceCIDRs empty
// admits any source address; MaxConns zero takes the default. Both are
// canonicalised by the store before they are persisted.
type IssueLeaseCredentialInput struct {
	LeaseID     string   `json:"leaseId"`
	SourceCIDRs []string `json:"sourceCidrs,omitempty"`
	MaxConns    int      `json:"maxConns,omitempty"`
}
