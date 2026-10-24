package store

import "fmt"

// PurgeResult counts the rows PurgeDemo removed from each collection.
type PurgeResult struct {
	Accounts int
	Nodes    int
	Leases   int
	Users    int
	Keys     int
	Requests int
	Audit    int
}

// Total returns the number of rows removed across every collection.
func (r PurgeResult) Total() int {
	return r.Accounts + r.Nodes + r.Leases + r.Users + r.Keys + r.Requests + r.Audit
}

// seedRowIDs are the identifiers build* in seed.go creates. PurgeDemo removes
// exactly these rows, so a live row that merely shares a collection survives.
// TestPurgeDemo locks these counts to the seed builders.
var (
	seedAccountIDs = seedIDSet("acc-", 6, 2)
	seedNodeIDs    = seedIDSet("node-", 12, 2)
	seedLeaseIDs   = seedIDSet("lease-", 8, 2)
	seedUserIDs    = seedIDSet("u-", 4, 2)
	seedKeyIDs     = seedIDSet("key-", 6, 2)
	seedRequestIDs = seedIDSet("req-", seedRequests, 3)
	seedAuditIDs   = seedIDSet("audit-", seedAudit, 3)
)

func seedIDSet(prefix string, count, width int) map[string]struct{} {
	ids := make(map[string]struct{}, count)
	for i := 1; i <= count; i++ {
		ids[fmt.Sprintf("%s%0*d", prefix, width, i)] = struct{}{}
	}
	return ids
}

// PurgeDemo removes the demo rows created by seed.go and leaves every row
// seed.go did not create untouched. A node is removed when it carries a seed
// id or belongs to a removed seed account, so a runtime node opened for a demo
// account cannot outlive its account; the removed account's sealed secret and
// device identity go with it, in the snapshot map and in the sealer's own file.
// Leases, users, keys, request rows and audit events are matched by their seed
// ids only: an account created after acc-06, a created key, a mutation-lab
// request row or a later audit event survives.
//
// The call is idempotent: a store without seed rows returns zero counts. An
// error is returned only when the sealer cannot delete a removed account's
// entry; the caller must then not save the snapshot, so a restart retries the
// purge with the accounts still present.
func (s *Store) PurgeDemo() (PurgeResult, error) {
	result, removedAccounts, sealer := s.purgeDemoLocked()
	// A sealer write may fsync, so the file-backed prune runs after the lock is
	// released. Seed accounts never carry a sealed secret in this build, but a
	// snapshot written by another build can, and a purged account must not
	// leave its envelope behind.
	if sealer != nil {
		for id := range removedAccounts {
			if err := sealer.Delete(id); err != nil {
				return result, fmt.Errorf("purge demo sealed secret for %q: %w", id, err)
			}
		}
	}
	return result, nil
}

// purgeDemoLocked applies the in-memory purge under the write lock and returns
// the removed account ids plus the configured sealer, so the caller can prune
// the sealer file without holding the lock.
func (s *Store) purgeDemoLocked() (PurgeResult, map[string]struct{}, Sealer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result PurgeResult

	removedAccounts := make(map[string]struct{}, len(seedAccountIDs))
	keptAccounts := s.accounts[:0]
	for _, account := range s.accounts {
		if _, seed := seedAccountIDs[account.ID]; seed {
			removedAccounts[account.ID] = struct{}{}
			result.Accounts++
			continue
		}
		keptAccounts = append(keptAccounts, account)
	}
	s.accounts = keptAccounts

	keptNodes := s.nodes[:0]
	for _, node := range s.nodes {
		_, seedNode := seedNodeIDs[node.ID]
		_, seedAccount := removedAccounts[node.AccountID]
		if seedNode || seedAccount {
			result.Nodes++
			continue
		}
		keptNodes = append(keptNodes, node)
	}
	s.nodes = keptNodes

	keptLeases := s.leases[:0]
	for _, lease := range s.leases {
		if _, seed := seedLeaseIDs[lease.ID]; seed {
			result.Leases++
			continue
		}
		keptLeases = append(keptLeases, lease)
	}
	s.leases = keptLeases

	keptUsers := s.users[:0]
	for _, user := range s.users {
		if _, seed := seedUserIDs[user.ID]; seed {
			result.Users++
			continue
		}
		keptUsers = append(keptUsers, user)
	}
	s.users = keptUsers

	keptKeys := s.keys[:0]
	for _, key := range s.keys {
		if _, seed := seedKeyIDs[key.ID]; seed {
			delete(s.keyHashes, key.ID)
			result.Keys++
			continue
		}
		keptKeys = append(keptKeys, key)
	}
	s.keys = keptKeys

	keptRequests := s.requests[:0]
	for _, request := range s.requests {
		if _, seed := seedRequestIDs[request.ID]; seed {
			result.Requests++
			continue
		}
		keptRequests = append(keptRequests, request)
	}
	s.requests = keptRequests

	keptAudit := s.audit[:0]
	for _, event := range s.audit {
		if _, seed := seedAuditIDs[event.ID]; seed {
			result.Audit++
			continue
		}
		keptAudit = append(keptAudit, event)
	}
	s.audit = keptAudit

	for id := range removedAccounts {
		delete(s.secrets, id)
		delete(s.devices, id)
	}
	s.syncAccountCountsLocked()
	return result, removedAccounts, s.sealer
}
