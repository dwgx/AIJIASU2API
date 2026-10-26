package webhook

// LeaseEventData returns the payload shared by lease.created, lease.reissued,
// lease.revoked and lease.expired. It carries the lease identity, the user and account it
// was allocated from, the platform and the expiry instant — and nothing else:
// a lease event never includes a key, token, password, signing secret or node
// credential. Callers pass the committed store row, so the same shape reaches
// the replay ring and every subscribed receiver.
func LeaseEventData(leaseID, userID, platform, accountID, expiresAt string) map[string]any {
	return map[string]any{
		"leaseId":   leaseID,
		"userId":    userID,
		"platform":  platform,
		"accountId": accountID,
		"expiresAt": expiresAt,
	}
}
