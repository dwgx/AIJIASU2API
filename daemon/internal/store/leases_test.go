package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// leaseTestFixture is one empty store with an active account, two bound LIVE
// nodes, one unbound READY control node and one enabled user. Tests add more
// users/nodes when a case needs them.
type leaseTestFixture struct {
	st      *Store
	account model.Account
	user    model.PanelUser
}

func newLeaseFixture(t *testing.T, now *time.Time, platform model.Platform, concurrency int) leaseTestFixture {
	t.Helper()
	// The shared fixture exercises allocation, not the default one-slot
	// guard; individual slot tests set AJS_ACCOUNT_SLOTS after construction.
	t.Setenv("AJS_ACCOUNT_SLOTS", "8")
	st := newEmptyWithClock(func() time.Time { return *now }, nil)
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label:       "Northwind Ops",
		Username:    "demo-northwind",
		Password:    "local-placeholder",
		Platform:    platform,
		Concurrency: concurrency,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.SetAccountUseCount(account.ID, 8); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-ready", AccountID: account.ID, Label: "cn-zj-01", Region: "cn-zj",
		State: model.NodeReady, EgressIP: "203.0.113.13", Protocol: model.ProtocolSOCKS5, GatePort: 13002,
	}); err != nil {
		t.Fatalf("PutNode(ready): %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-live", AccountID: account.ID, Label: "cn-sh-01", Region: "cn-sh",
		State: model.NodeLive, EgressIP: "203.0.113.12", Protocol: model.ProtocolSOCKS5, GatePort: 13001,
	}); err != nil {
		t.Fatalf("PutNode(live): %v", err)
	}
	// A second bound node gives the fixture two serving slots without
	// changing the READY row's role: READY is never a lease target.
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-live-2", AccountID: account.ID, Label: "cn-hk-01", Region: "cn-hk",
		State: model.NodeLive, EgressIP: "203.0.113.14", Protocol: model.ProtocolSOCKS5, GatePort: 13003,
	}); err != nil {
		t.Fatalf("PutNode(live-2): %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "ada.ops"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return leaseTestFixture{st: st, account: account, user: user}
}

func addLeaseAccount(t *testing.T, st *Store, label, username string) model.Account {
	t.Helper()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: label, Username: username, Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("CreateAccount(%q): %v", label, err)
	}
	return account
}

func addLeaseUser(t *testing.T, st *Store, name string, quota *int64) model.PanelUser {
	t.Helper()
	user, err := st.CreateUser(model.CreateUserInput{Name: name, QuotaBytesMonth: quota})
	if err != nil {
		t.Fatalf("CreateUser(%q): %v", name, err)
	}
	return user
}

func addLeaseNode(t *testing.T, st *Store, accountID, id, label string) model.ProxyNode {
	t.Helper()
	node, err := st.PutNode(model.ProxyNode{
		ID: id, AccountID: accountID, Label: label, Region: "cn-zj",
		State: model.NodeLive, EgressIP: "203.0.113.20", Protocol: model.ProtocolSOCKS5, GatePort: 13020,
	})
	if err != nil {
		t.Fatalf("PutNode(%q): %v", id, err)
	}
	return node
}

// TestCreateLeaseAllocatesLiveNodeAndReplaysIdempotency pins the allocation
// order (LIVE before READY), the default TTL and the retry contract: the same
// key returns the original row instead of allocating a second node.
func TestCreateLeaseAllocatesLiveNodeAndReplaysIdempotency(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 2)
	st := fixture.st

	lease, replayed, err := st.CreateLease(model.CreateLeaseInput{
		UserID:         fixture.user.ID,
		Platform:       model.PlatformAndroid,
		IdempotencyKey: "retry-1",
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if replayed {
		t.Fatal("first create reported a replay")
	}
	if lease.ID != "lease-01" || lease.Status != model.LeaseActive || lease.Platform != model.PlatformAndroid {
		t.Fatalf("lease = %+v, want lease-01 active android", lease)
	}
	if lease.NodeID != "node-live" {
		t.Fatalf("allocated node = %q, want node-live (LIVE preferred)", lease.NodeID)
	}
	if lease.TTLSeconds != DefaultLeaseTTLSeconds || lease.CreatedAt != iso(now) {
		t.Fatalf("lease timing = %+v, want default 8h from now", lease)
	}
	if lease.ExpiresAt != iso(now.Add(DefaultLeaseTTLSeconds*time.Second)) {
		t.Fatalf("expiresAt = %q, want now+8h", lease.ExpiresAt)
	}

	replay, replayed, err := st.CreateLease(model.CreateLeaseInput{
		UserID:         fixture.user.ID,
		Platform:       model.PlatformAndroid,
		IdempotencyKey: "retry-1",
	})
	if err != nil {
		t.Fatalf("replayed CreateLease: %v", err)
	}
	if !replayed || replay.ID != lease.ID {
		t.Fatalf("replay = %+v replayed=%v, want %s", replay, replayed, lease.ID)
	}
	if got := len(st.Leases()); got != 1 {
		t.Fatalf("leases after replay = %d, want 1", got)
	}

	other := addLeaseUser(t, st, "build-bot", nil)
	_, _, err = st.CreateLease(model.CreateLeaseInput{
		UserID: other.ID, Platform: model.PlatformAndroid, IdempotencyKey: "retry-1",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("reused key for another user = %v, want ErrConflict", err)
	}

	node, _ := st.Node("node-live")
	if node.InUse != 1 {
		t.Fatalf("node-live inUse = %d, want 1", node.InUse)
	}
}

// TestCreateLeaseRefusesDuplicateUserPlatform covers the 409 guard: one active
// lease per user and platform, while another platform still allocates.
func TestCreateLeaseRefusesDuplicateUserPlatform(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 4)
	st := fixture.st
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid,
	}); err != nil {
		t.Fatalf("first CreateLease: %v", err)
	}

	_, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid,
	})
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) || leaseErr.Reason != "lease_conflict" {
		t.Fatalf("duplicate create = %v, want lease_conflict", err)
	}

	ios, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Field Lab", Username: "demo-fieldlab", Password: "local-placeholder",
		Platform: model.PlatformIOS, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("CreateAccount(ios): %v", err)
	}
	addLeaseNode(t, st, ios.ID, "node-ios", "jp-tyo-01")
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformIOS,
	}); err != nil {
		t.Fatalf("same user on another platform: %v", err)
	}
	if got := len(st.Leases()); got != 2 {
		t.Fatalf("leases = %d, want 2", got)
	}
}

// TestCreateLeaseEnforcesUserQuotaAndAccountConcurrency covers the two 429
// guards: a zero monthly quota refuses the first lease, and a concurrency of
// one refuses the second user on the same account.
func TestCreateLeaseEnforcesUserQuotaAndAccountConcurrency(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 1)
	st := fixture.st

	quotaUser := addLeaseUser(t, st, "quota-zero", ptrInt64(0))
	_, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: quotaUser.ID, Platform: model.PlatformAndroid,
	})
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) || !errors.Is(err, ErrQuota) || leaseErr.Reason != "quota_exceeded" {
		t.Fatalf("quota-zero create = %v, want quota_exceeded", err)
	}

	first := addLeaseUser(t, st, "first-bot", nil)
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: first.ID, Platform: model.PlatformAndroid,
	}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	second := addLeaseUser(t, st, "second-bot", nil)
	_, _, err = st.CreateLease(model.CreateLeaseInput{
		UserID: second.ID, Platform: model.PlatformAndroid,
	})
	if !errors.As(err, &leaseErr) || leaseErr.Reason != "concurrency_exceeded" {
		t.Fatalf("concurrency create = %v, want concurrency_exceeded", err)
	}

	// A second free node does not lift the account's concurrency of one.
	addLeaseNode(t, st, fixture.account.ID, "node-extra", "cn-zj-02")
	_, _, err = st.CreateLease(model.CreateLeaseInput{
		UserID: second.ID, Platform: model.PlatformAndroid,
	})
	if !errors.As(err, &leaseErr) || leaseErr.Reason != "concurrency_exceeded" {
		t.Fatalf("concurrency create with free node = %v, want concurrency_exceeded", err)
	}
}

// TestCreateLeaseNoCapacityAndSuspendedUser covers the remaining refusals.
func TestCreateLeaseNoCapacityAndSuspendedUser(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 4)
	st := fixture.st

	suspended := addLeaseUser(t, st, "suspended-bot", nil)
	if _, err := st.SetUserEnabled(suspended.ID, false); err != nil {
		t.Fatalf("SetUserEnabled: %v", err)
	}
	_, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: suspended.ID, Platform: model.PlatformAndroid,
	})
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) || leaseErr.Reason != "user_disabled" {
		t.Fatalf("suspended create = %v, want user_disabled", err)
	}

	// Fill the node-bearing account (both of its slot capacity), then add an
	// active account with no node. The next user sees no_capacity rather than a
	// slot refusal because every account is either capacity-full or node-less.
	for index := 1; index <= 2; index++ {
		holder := addLeaseUser(t, st, "holder-"+strconv.Itoa(index), nil)
		if _, _, err := st.CreateLease(model.CreateLeaseInput{
			UserID: holder.ID, Platform: model.PlatformAndroid,
		}); err != nil {
			t.Fatalf("holder %d create: %v", index, err)
		}
	}
	addLeaseAccount(t, st, "Empty Ops", "empty-ops")
	third := addLeaseUser(t, st, "third-bot", nil)
	_, _, err = st.CreateLease(model.CreateLeaseInput{
		UserID: third.ID, Platform: model.PlatformAndroid,
	})
	if !errors.As(err, &leaseErr) || leaseErr.Reason != "no_capacity" {
		t.Fatalf("no-capacity create = %v, want no_capacity", err)
	}

	// An unknown platform is a 400-class refusal, not a capacity one.
	_, _, err = st.CreateLease(model.CreateLeaseInput{
		UserID: third.ID, Platform: model.Platform("solaris"),
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid platform create = %v, want ErrInvalid", err)
	}
}

// TestRevokeAndExpireReleaseTheNode covers the lifecycle: revoke returns the
// row as revoked and frees the node; a lease whose TTL passed is swept on the
// next read and writes a lease.expired audit row.
func TestRevokeAndExpireReleaseTheNode(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 4)
	st := fixture.st

	lease, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid, TTLSeconds: 60,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	revoked, err := st.RevokeLease(lease.ID)
	if err != nil {
		t.Fatalf("RevokeLease: %v", err)
	}
	if revoked.Status != model.LeaseRevoked {
		t.Fatalf("revoked status = %q, want revoked", revoked.Status)
	}
	if _, ok := st.Lease(lease.ID); ok {
		t.Fatal("revoked lease is still readable")
	}
	node, _ := st.Node("node-live")
	if node.InUse != 0 {
		t.Fatalf("node-live inUse after revoke = %d, want 0", node.InUse)
	}
	if err := st.ReleaseLease(lease.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReleaseLease on revoked row = %v, want ErrNotFound", err)
	}

	// Expiry: the same user may lease again after the first row expired.
	second, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid, TTLSeconds: 60,
	})
	if err != nil {
		t.Fatalf("second CreateLease: %v", err)
	}
	now = now.Add(61 * time.Second)
	if leases := st.Leases(); len(leases) != 0 {
		t.Fatalf("leases after expiry = %+v, want none", leases)
	}
	node, _ = st.Node(second.NodeID)
	if node.InUse != 0 {
		t.Fatalf("node inUse after expiry = %d, want 0", node.InUse)
	}
	events := st.Audit(3)
	found := false
	for _, event := range events {
		if event.Action == "lease.expired" && strings.Contains(event.Detail, second.NodeLabel) {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit = %+v, want a lease.expired row", events)
	}
}

// TestCreatedLeaseSurvivesSnapshot proves the new fields are persisted and
// restored, so a restart does not turn an active lease into an empty-status
// row or drop its idempotency key.
func TestCreatedLeaseSurvivesSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 2)
	created, _, err := fixture.st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid, IdempotencyKey: "snap-1",
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	data, err := fixture.st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return now }, nil)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	leases := restored.Leases()
	if len(leases) != 1 {
		t.Fatalf("restored leases = %+v, want one", leases)
	}
	if leases[0].ID != created.ID || leases[0].Status != model.LeaseActive ||
		leases[0].IdempotencyKey != "snap-1" || leases[0].Platform != model.PlatformAndroid {
		t.Fatalf("restored lease = %+v, want the created row", leases[0])
	}
	replay, replayed, err := restored.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid, IdempotencyKey: "snap-1",
	})
	if err != nil || !replayed || replay.ID != created.ID {
		t.Fatalf("replay after restore = %+v replayed=%v err=%v", replay, replayed, err)
	}
}

// TestLeaseExpiryHookFiresOncePerLease proves the sweep reports each expired
// lease exactly once with the event contract fields, and never reports a
// lease that was revoked before its TTL passed.
func TestLeaseExpiryHookFiresOncePerLease(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 4)
	st := fixture.st

	var expired []model.Lease
	st.SetLeaseExpiryHook(func(lease model.Lease) {
		expired = append(expired, lease)
	})

	first, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid, TTLSeconds: 60,
	})
	if err != nil {
		t.Fatalf("CreateLease(first): %v", err)
	}
	secondAccount := addLeaseAccount(t, st, "Grace Ops", "grace-ops")
	addLeaseNode(t, st, secondAccount.ID, "node-grace", "grace-01")
	grace := addLeaseUser(t, st, "grace.ops", nil)
	revoked, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: grace.ID, Platform: model.PlatformAndroid, TTLSeconds: 60,
	})
	if err != nil {
		t.Fatalf("CreateLease(grace): %v", err)
	}
	if _, err := st.RevokeLease(revoked.ID); err != nil {
		t.Fatalf("RevokeLease: %v", err)
	}

	now = now.Add(61 * time.Second)
	if leases := st.Leases(); len(leases) != 0 {
		t.Fatalf("leases after expiry = %+v, want none", leases)
	}
	// A second read sweeps again; the released row is gone, so the hook must
	// not fire a duplicate.
	if leases := st.Leases(); len(leases) != 0 {
		t.Fatalf("leases after second sweep = %+v, want none", leases)
	}

	if len(expired) != 1 {
		t.Fatalf("expiry hook fired %d times (%+v), want once for %s", len(expired), expired, first.ID)
	}
	got := expired[0]
	if got.ID != first.ID || got.UserID != fixture.user.ID ||
		got.AccountID != fixture.account.ID || got.Platform != model.PlatformAndroid ||
		got.ExpiresAt != first.ExpiresAt || got.Status != model.LeaseExpired {
		t.Fatalf("expiry hook lease = %+v, want the expired %s", got, first.ID)
	}

	// Removing the hook keeps the sweep working without a callback.
	st.SetLeaseExpiryHook(nil)
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid, TTLSeconds: 60,
	}); err != nil {
		t.Fatalf("CreateLease(after hook removal): %v", err)
	}
	now = now.Add(61 * time.Second)
	if leases := st.Leases(); len(leases) != 0 {
		t.Fatalf("leases after hook removal sweep = %+v, want none", leases)
	}
	if len(expired) != 1 {
		t.Fatalf("removed hook fired again: %+v", expired)
	}
}

// TestDemoteNodeRecordsDurableLeaseEnd proves the snapshot carries the
// restart outcome after the active row is gone: the same lease id, user,
// node, slot/generation and the daemon_restart reason survive a save/load
// cycle. A lease end is not an active lease, so Leases() stays empty.
func TestDemoteNodeRecordsDurableLeaseEnd(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 1)
	st := fixture.st
	lease, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}

	node, revoked, changed, err := st.DemoteNode(lease.NodeID)
	if err != nil || !changed || node.State != model.NodeStale {
		t.Fatalf("DemoteNode = %+v/%+v/%v/%v, want a changed STALE node", node, revoked, changed, err)
	}
	if len(revoked) != 1 || revoked[0].ID != lease.ID || revoked[0].Status != model.LeaseRevoked {
		t.Fatalf("revoked = %+v, want one revoked %s", revoked, lease.ID)
	}
	ends := st.LeaseEnds()
	if len(ends) != 1 || ends[0].Lease.ID != lease.ID || ends[0].Reason != "daemon_restart" ||
		ends[0].Lease.Status != model.LeaseRevoked {
		t.Fatalf("lease ends = %+v, want one daemon_restart row for %s", ends, lease.ID)
	}

	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snap.LeaseEnds) != 1 || snap.LeaseEnds[0].Lease.ID != lease.ID ||
		snap.LeaseEnds[0].Reason != "daemon_restart" {
		t.Fatalf("snapshot leaseEnds = %+v, want the daemon_restart row", snap.LeaseEnds)
	}

	restored := newEmptyWithClock(func() time.Time { return now }, nil)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if leases := restored.Leases(); len(leases) != 0 {
		t.Fatalf("restored active leases = %+v, want none", leases)
	}
	restoredEnds := restored.LeaseEnds()
	if len(restoredEnds) != 1 || restoredEnds[0].Lease.ID != lease.ID ||
		restoredEnds[0].Reason != "daemon_restart" || restoredEnds[0].EndedAt != ends[0].EndedAt {
		t.Fatalf("restored lease ends = %+v, want the durable outcome", restoredEnds)
	}
	again, err := restored.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot after restore: %v", err)
	}
	if !bytes.Equal(data, again) {
		t.Fatalf("leaseEnds snapshot round-trip is not byte-identical:\nfirst:\n%s\nsecond:\n%s", data, again)
	}
}

// TestLeaseRevokeHookFlushesPendingRestartRows pins the startup ordering: the
// recovery path revokes before the webhook sink exists, so the rows wait in
// memory and are delivered exactly once when the hook is installed.
func TestLeaseRevokeHookFlushesPendingRestartRows(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 1)
	st := fixture.st
	lease, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if _, _, _, err := st.DemoteNode(lease.NodeID); err != nil {
		t.Fatalf("DemoteNode: %v", err)
	}

	var events []model.Lease
	st.SetLeaseRevokeHook(func(got model.Lease) { events = append(events, got) })
	if len(events) != 1 || events[0].ID != lease.ID || events[0].Status != model.LeaseRevoked {
		t.Fatalf("pending hook events = %+v, want one revoked %s", events, lease.ID)
	}
	// A second installation must not replay the already delivered row.
	st.SetLeaseRevokeHook(func(got model.Lease) { events = append(events, got) })
	if len(events) != 1 {
		t.Fatalf("hook replayed a delivered row: %+v", events)
	}
}

// TestLeaseIDsDoNotReuseEndedIDs pins the outcome log's id space: a revoked
// lease id remains in leaseEnds, so a new active lease must skip it instead
// of creating a snapshot with the same id in both collections.
func TestLeaseIDsDoNotReuseEndedIDs(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 2)
	st := fixture.st
	first, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid,
	})
	if err != nil {
		t.Fatalf("CreateLease(first): %v", err)
	}
	if _, _, _, err := st.DemoteNode(first.NodeID); err != nil {
		t.Fatalf("DemoteNode: %v", err)
	}
	second, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid,
	})
	if err != nil {
		t.Fatalf("CreateLease(second): %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("new lease reused ended id %s", first.ID)
	}
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := newEmptyWithClock(func() time.Time { return now }, nil)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore with active lease + leaseEnds: %v", err)
	}
	if leases := restored.Leases(); len(leases) != 1 || leases[0].ID != second.ID {
		t.Fatalf("restored active leases = %+v, want %s", leases, second.ID)
	}
}
