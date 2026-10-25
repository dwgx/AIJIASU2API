package model

// TenantStatus is the customer tenant lifecycle. A pending tenant owns no
// PanelUser, lease or key; approval links exactly one user and activates it.
// Suspension disables that user and revokes the tenant's sessions and keys.
type TenantStatus string

const (
	TenantPending   TenantStatus = "pending"
	TenantActive    TenantStatus = "active"
	TenantSuspended TenantStatus = "suspended"
)

// ValidTenantStatus reports whether status is part of the closed vocabulary.
func ValidTenantStatus(status TenantStatus) bool {
	switch status {
	case TenantPending, TenantActive, TenantSuspended:
		return true
	default:
		return false
	}
}

// Tenant is the portal login identity. It is deliberately separate from
// PanelUser: the user owns leases, usage and API keys, while the tenant only
// proves who may reach them. The password hash and session material are
// store-only and never part of this type.
type Tenant struct {
	ID          string       `json:"id"`
	Email       string       `json:"email"`
	DisplayName string       `json:"displayName"`
	Status      TenantStatus `json:"status"`
	UserID      *string      `json:"userId"`
	CreatedAt   string       `json:"createdAt"`
	ApprovedAt  *string      `json:"approvedAt"`
	SuspendedAt *string      `json:"suspendedAt"`
}

// TenantSignupInput is the POST /v1/portal/signup body. Signup stores a
// pending tenant only; the password is hashed with Argon2id and the raw value
// is discarded.
type TenantSignupInput struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

// TenantLoginInput is the POST /v1/portal/login body.
type TenantLoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// PortalSession is one persisted portal session. IDHash and CSRFHash are
// SHA-256 hex digests; the raw cookie and CSRF values are never persisted.
// CSRFToken is the process-only raw token so GET /v1/portal/session can return
// it after a reload; a snapshot drops it and the next session read rotates a
// fresh token.
type PortalSession struct {
	IDHash     string  `json:"idHash"`
	TenantID   string  `json:"tenantId"`
	CSRFHash   string  `json:"csrfHash"`
	CSRFToken  string  `json:"-"`
	CreatedAt  string  `json:"createdAt"`
	LastSeenAt string  `json:"lastSeenAt"`
	ExpiresAt  string  `json:"expiresAt"`
	RevokedAt  *string `json:"revokedAt"`
}

// TenantSessionView is the login response and the GET /v1/portal/session
// response. User is the linked PanelUser; it is null only for a tenant whose
// link is broken, which the auth path refuses before this view is written.
type TenantSessionView struct {
	Tenant    Tenant     `json:"tenant"`
	User      *PanelUser `json:"user"`
	CSRFToken string     `json:"csrfToken"`
}

// TenantKeyCreateRequest is the POST /v1/portal/keys body.
type TenantKeyCreateRequest struct {
	Label string `json:"label"`
}

// TenantApproveRequest is the POST /v1/admin/tenants/{id}/approve body. Supply
// exactly one of UserID (bind an existing user) or User (create and bind one
// identity in the same commit).
type TenantApproveRequest struct {
	UserID string           `json:"userId"`
	User   *CreateUserInput `json:"user"`
}
