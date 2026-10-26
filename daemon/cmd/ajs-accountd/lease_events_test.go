package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
)

// TestEmitLeaseExpiryMapsOntoWebhookCatalogue pins the wire name, the exact
// payload (ids, platform and expiry only, never a credential) and the nil-sink
// rule.
func TestEmitLeaseExpiryMapsOntoWebhookCatalogue(t *testing.T) {
	sink := &renewalRecordingSink{}
	emitLeaseExpiry(sink, model.Lease{
		ID: "lease-03", UserID: "user-01", Platform: model.PlatformIOS,
		AccountID: "acc-02", ExpiresAt: "2026-10-25T20:00:00Z",
	})
	emitLeaseExpiry(nil, model.Lease{ID: "lease-ignored"})

	want := []renewalSinkCall{{event: webhook.EventLeaseExpired, data: map[string]any{
		"leaseId":   "lease-03",
		"userId":    "user-01",
		"platform":  "ios",
		"accountId": "acc-02",
		"expiresAt": "2026-10-25T20:00:00Z",
	}}}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
}

// TestLeaseSweepReachesWebhookSink drives the same hook run() installs: a real
// store sweep over an expired lease must reach the sink exactly once, and a
// second sweep must not duplicate the event.
func TestLeaseSweepReachesWebhookSink(t *testing.T) {
	st := store.NewEmpty()
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Lease Wire", Username: "lease-user", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: "node-live", AccountID: account.ID, Label: "cn-sh-01", Region: "cn-sh",
		State: model.NodeLive, EgressIP: "203.0.113.12", Protocol: model.ProtocolSOCKS5, GatePort: 13001,
	}); err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "ada.ops"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	lease, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, TTLSeconds: 60,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}

	// Move the committed lease's expiry into the past through the snapshot the
	// daemon itself writes, then restore it: no clock hook is exported to cmd.
	raw, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var snap store.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snap.Leases) != 1 || snap.Leases[0].ID != lease.ID {
		t.Fatalf("snapshot leases = %+v, want %s", snap.Leases, lease.ID)
	}
	snap.Leases[0].ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	edited, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	restored := store.NewEmpty()
	if err := restored.Restore(edited); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	sink := &renewalRecordingSink{}
	restored.SetLeaseExpiryHook(func(expired model.Lease) {
		emitLeaseExpiry(sink, expired)
	})

	if leases := restored.Leases(); len(leases) != 0 {
		t.Fatalf("leases after first sweep = %+v, want none", leases)
	}
	if leases := restored.Leases(); len(leases) != 0 {
		t.Fatalf("leases after second sweep = %+v, want none", leases)
	}

	want := []renewalSinkCall{{event: webhook.EventLeaseExpired, data: map[string]any{
		"leaseId":   lease.ID,
		"userId":    user.ID,
		"platform":  "android",
		"accountId": account.ID,
		"expiresAt": snap.Leases[0].ExpiresAt,
	}}}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
}

// TestEmitLeaseRevokeMapsOntoWebhookCatalogue pins the restart/restore event
// wire name and payload. The store queues revocations before the webhook sink
// exists; the daemon flushes them through this mapping as lease.revoked.
func TestEmitLeaseRevokeMapsOntoWebhookCatalogue(t *testing.T) {
	sink := &renewalRecordingSink{}
	emitLeaseRevoke(sink, model.Lease{
		ID: "lease-04", UserID: "user-02", Platform: model.PlatformWindows,
		AccountID: "acc-03", ExpiresAt: "2026-10-26T04:00:00Z", Status: model.LeaseRevoked,
	})
	emitLeaseRevoke(nil, model.Lease{ID: "lease-ignored"})

	want := []renewalSinkCall{{event: webhook.EventLeaseRevoked, data: map[string]any{
		"leaseId":   "lease-04",
		"userId":    "user-02",
		"platform":  "windows",
		"accountId": "acc-03",
		"expiresAt": "2026-10-26T04:00:00Z",
	}}}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
}
