package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

func TestCatalogSelectionPersists(t *testing.T) {
	st := New()
	if _, err := st.SetSelection("acc-01", "srv-a", "operator"); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	selection, ok := st.Selection("acc-01")
	if !ok || selection.ServerID != "srv-a" || selection.UpdatedBy != "operator" || selection.UpdatedAt == "" {
		t.Fatalf("selection = %+v/%v", selection, ok)
	}
	event := st.Audit(1000)[0]
	if event.Action != "catalog.select" || event.Meta["serverId"] != "srv-a" {
		t.Fatalf("audit = %+v, want catalog.select", event)
	}
	if _, ok := event.Meta["previousServerId"]; ok {
		t.Fatalf("first select recorded a previous server: %v", event.Meta)
	}

	// Setting the same target again is a no-op and writes no audit.
	audits := len(st.Audit(1000))
	if _, err := st.SetSelection("acc-01", "srv-a", "operator"); err != nil {
		t.Fatalf("SetSelection same: %v", err)
	}
	if got := len(st.Audit(1000)); got != audits {
		t.Fatalf("same-target audits = %d, want %d", got, audits)
	}

	if _, err := st.SetSelection("acc-01", "srv-b", "operator"); err != nil {
		t.Fatalf("SetSelection b: %v", err)
	}
	event = st.Audit(1000)[0]
	if event.Action != "catalog.select" || event.Meta["previousServerId"] != "srv-a" || event.Meta["serverId"] != "srv-b" {
		t.Fatalf("audit = %+v, want previousServerId srv-a", event)
	}

	// The target survives a snapshot/restore roundtrip.
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	restored := New()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got, ok := restored.Selection("acc-01"); !ok || got.ServerID != "srv-b" {
		t.Fatalf("restored selection = %+v/%v", got, ok)
	}

	// Deleting the account cascades the selection.
	if err := restored.DeleteAccount("acc-01"); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if _, ok := restored.Selection("acc-01"); ok {
		t.Fatalf("DeleteAccount kept the selection")
	}
}

func TestCatalogSelectionClearAndLost(t *testing.T) {
	st := New()
	if _, err := st.SetSelection("acc-01", "srv-a", "operator"); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	cleared, err := st.ClearSelection("acc-01", "operator")
	if err != nil || !cleared {
		t.Fatalf("ClearSelection = %v/%v", cleared, err)
	}
	if event := st.Audit(1000)[0]; event.Action != "catalog.unselect" || event.Meta["previousServerId"] != "srv-a" {
		t.Fatalf("audit = %+v, want catalog.unselect", event)
	}
	audits := len(st.Audit(1000))
	cleared, err = st.ClearSelection("acc-01", "operator")
	if err != nil || cleared {
		t.Fatalf("second ClearSelection = %v/%v, want false/nil", cleared, err)
	}
	if got := len(st.Audit(1000)); got != audits {
		t.Fatalf("idempotent clear wrote an audit")
	}

	if _, err := st.SetSelection("acc-01", "srv-b", "operator"); err != nil {
		t.Fatalf("SetSelection b: %v", err)
	}
	lost, err := st.LoseSelection("acc-01", "server_missing")
	if err != nil || !lost {
		t.Fatalf("LoseSelection = %v/%v", lost, err)
	}
	event := st.Audit(1000)[0]
	if event.Action != "catalog.selection_lost" || event.Actor != "system" ||
		event.Meta["serverId"] != "srv-b" || event.Meta["reason"] != "server_missing" {
		t.Fatalf("audit = %+v, want catalog.selection_lost", event)
	}
}

func TestCatalogCommitSwitch(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	st := newEmptyWithClock(func() time.Time { return now }, nil)
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Switch Ops", Username: "switch-ops", Password: "local-placeholder",
		Platform: model.PlatformAndroid, Concurrency: 8,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.SetAccountUseCount(account.ID, 2); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}
	key := model.SlotKey{AccountID: account.ID, Index: 1}
	oldID := key.NodeID("old")
	if err := st.UpsertSlot(model.AccountSlot{
		SlotKey: key, ServerID: "old", GatePort: 13020,
		State: model.SlotLive, Health: model.SlotHealthHealthy, Generation: 1,
	}); err != nil {
		t.Fatalf("UpsertSlot: %v", err)
	}
	if _, err := st.PutNode(model.ProxyNode{
		ID: oldID, AccountID: account.ID, Label: "cn-switch-old", Region: "cn",
		State: model.NodeLive, GatePort: 13020, ServerID: "old", Slot: 1,
		Capacity: 32, Protocol: model.ProtocolSOCKS5,
	}); err != nil {
		t.Fatalf("PutNode(old): %v", err)
	}
	// A legacy row (Slot 0) must survive a slot-1 switch: it is never
	// collateral for a slot-scoped commit.
	if _, err := st.PutNode(model.ProxyNode{
		ID: "legacy-node", AccountID: account.ID, Label: "cn-legacy", Region: "cn",
		State: model.NodeReady, GatePort: 13021, Capacity: 32, Protocol: model.ProtocolSOCKS5,
	}); err != nil {
		t.Fatalf("PutNode(legacy): %v", err)
	}
	user, err := st.CreateUser(model.CreateUserInput{Name: "switch-user"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	lease, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: user.ID, Platform: model.PlatformAndroid, AccountID: account.ID,
	})
	if err != nil {
		t.Fatalf("CreateLease: %v", err)
	}
	if lease.NodeID != oldID || lease.Slot != 1 || lease.Generation != 1 {
		t.Fatalf("lease = %+v, want node %s slot 1 generation 1", lease, oldID)
	}

	selection := &model.Selection{AccountID: account.ID, ServerID: "new", UpdatedBy: "operator"}
	latency := 12
	stat := &model.ServerStat{AccountID: account.ID, ServerID: "new", LastLatencyMs: &latency}
	newID := key.NodeID("new")
	node, auditID, err := st.CommitSwitch(SwitchCommit{
		Node: model.ProxyNode{
			ID: newID, AccountID: account.ID, Label: "cn-switch-new", Region: "cn",
			State: model.NodeLive, GatePort: 13020, ServerID: "new", Slot: 1,
			Capacity: 32, Protocol: model.ProtocolSOCKS5,
		},
		Slot: key,
		SlotState: &model.AccountSlot{
			SlotKey: key, ServerID: "new", GatePort: 13020,
			State: model.SlotLive, Health: model.SlotHealthHealthy, Generation: 2,
		},
		Actor: "operator", Action: "catalog.switch", Target: newID,
		Detail:    "Switched · one session live",
		Meta:      map[string]string{"fromServerId": "old", "toServerId": "new", "result": "ok"},
		Selection: selection,
		Stat:      stat,
	})
	if err != nil {
		t.Fatalf("CommitSwitch: %v", err)
	}
	if node.ID != newID || node.State != model.NodeLive || node.AccountLabel != "Switch Ops" {
		t.Fatalf("node = %+v", node)
	}
	if node.InUse != 1 {
		t.Fatalf("node inUse = %d, want 1 (the rebound lease)", node.InUse)
	}
	if _, ok := st.Node(oldID); ok {
		t.Fatalf("old slot node %q survived the switch", oldID)
	}
	if _, ok := st.Node("legacy-node"); !ok {
		t.Fatal("legacy Slot 0 node was deleted by a slot-scoped switch")
	}
	leases := st.Leases()
	if len(leases) != 1 {
		t.Fatalf("leases = %d, want the one rebound lease", len(leases))
	}
	rebound := leases[0]
	if rebound.NodeID != newID || rebound.NodeLabel != "cn-switch-new" || rebound.GatePort != 13020 ||
		rebound.Slot != 1 || rebound.Generation != 2 {
		t.Fatalf("rebound lease = %+v, want node %s slot 1 generation 2", rebound, newID)
	}
	slot, ok := st.Slot(key)
	if !ok || slot.ServerID != "new" || slot.State != model.SlotLive || slot.Generation != 2 {
		t.Fatalf("slot = %+v/%v, want server new LIVE generation 2", slot, ok)
	}
	accountAfter, _ := st.Account(account.ID)
	if accountAfter.NodeCount != 2 || accountAfter.LiveNodeCount != 1 {
		t.Fatalf("account counts = %d/%d, want 2/1 (new LIVE plus the legacy READY row)",
			accountAfter.NodeCount, accountAfter.LiveNodeCount)
	}
	gotSelection, ok := st.Selection(account.ID)
	if !ok || gotSelection.ServerID != "new" {
		t.Fatalf("selection = %+v/%v", gotSelection, ok)
	}
	gotStat, ok := st.ServerStat(account.ID, "new")
	if !ok || gotStat.Attempts != 1 || gotStat.Successes != 1 || gotStat.LastLatencyMs == nil || *gotStat.LastLatencyMs != 12 {
		t.Fatalf("stat = %+v/%v", gotStat, ok)
	}
	event := st.Audit(1000)[0]
	if event.ID != auditID || event.Action != "catalog.switch" || event.Meta["fromServerId"] != "old" {
		t.Fatalf("audit = %+v, want audit %s", event, auditID)
	}
}

func TestCatalogCommitSwitchFailureKeepsState(t *testing.T) {
	st := New()
	beforeAudits := len(st.Audit(1000))
	beforeNodes := 0
	for _, node := range st.Nodes() {
		if node.AccountID == "acc-01" {
			beforeNodes++
		}
	}
	beforeLeases := 0
	for _, lease := range st.Leases() {
		if lease.AccountID == "acc-01" {
			beforeLeases++
		}
	}

	_, _, err := st.CommitSwitch(SwitchCommit{
		Node: model.ProxyNode{
			// node-04 belongs to acc-02 and already owns this label.
			ID: "acc-01:new", AccountID: "acc-01", Label: "jp-tyo-01", Region: "cn", State: model.NodeLive,
			GatePort: 13020, ServerID: "new", Slot: 1, Capacity: 32, Protocol: model.ProtocolSOCKS5,
		},
		Actor: "operator", Action: "catalog.switch", Target: "acc-01:new",
		Detail: "Switched", Stat: &model.ServerStat{AccountID: "acc-01", ServerID: "new"},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("CommitSwitch error = %v, want conflict", err)
	}
	afterNodes := 0
	for _, node := range st.Nodes() {
		if node.AccountID == "acc-01" {
			afterNodes++
		}
	}
	afterLeases := 0
	for _, lease := range st.Leases() {
		if lease.AccountID == "acc-01" {
			afterLeases++
		}
	}
	if afterNodes != beforeNodes || afterLeases != beforeLeases {
		t.Fatalf("failed commit mutated nodes/leases: %d/%d -> %d/%d", beforeNodes, beforeLeases, afterNodes, afterLeases)
	}
	if _, ok := st.Selection("acc-01"); ok {
		t.Fatalf("failed commit persisted a selection")
	}
	if _, ok := st.ServerStat("acc-01", "new"); ok {
		t.Fatalf("failed commit wrote a stat")
	}
	if got := len(st.Audit(1000)); got != beforeAudits {
		t.Fatalf("failed commit wrote an audit: %d -> %d", beforeAudits, got)
	}
}

// TestCatalogCommitSwitchRejectsLegacySlot proves the store never infers a slot
// from the id or the account: a commit whose node still carries Slot 0 is a
// caller bug, not an account-wide switch.
func TestCatalogCommitSwitchRejectsLegacySlot(t *testing.T) {
	st := New()
	_, _, err := st.CommitSwitch(SwitchCommit{
		Node: model.ProxyNode{
			ID: "acc-01:new", AccountID: "acc-01", Label: "cn-test-legacy", Region: "cn",
			State: model.NodeLive, GatePort: 13020, ServerID: "new",
			Capacity: 32, Protocol: model.ProtocolSOCKS5,
		},
		Actor: "operator", Action: "catalog.switch", Target: "acc-01:new",
		Detail: "Switched", Stat: &model.ServerStat{AccountID: "acc-01", ServerID: "new"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("CommitSwitch error = %v, want ErrInvalid", err)
	}
	if _, ok := st.Node("acc-01:new"); ok {
		t.Fatal("rejected commit wrote the node")
	}
}

func TestCatalogQuietNodeHelpers(t *testing.T) {
	st := New()
	audits := len(st.Audit(1000))
	if err := st.SetNodeStateQuiet("node-01", model.NodeDraining); err != nil {
		t.Fatalf("SetNodeStateQuiet: %v", err)
	}
	node, _ := st.Node("node-01")
	if node.State != model.NodeDraining {
		t.Fatalf("state = %q, want DRAINING", node.State)
	}
	if err := st.DeleteNodeQuiet("node-01"); err != nil {
		t.Fatalf("DeleteNodeQuiet: %v", err)
	}
	if _, ok := st.Node("node-01"); ok {
		t.Fatalf("quiet delete kept the node")
	}
	for _, lease := range st.Leases() {
		if lease.NodeID == "node-01" {
			t.Fatalf("quiet delete kept lease %s", lease.ID)
		}
	}
	if got := len(st.Audit(1000)); got != audits {
		t.Fatalf("quiet helpers wrote %d audits", got-audits)
	}
}

func TestCatalogSnapshotAdditive(t *testing.T) {
	st := New()
	if err := st.Restore([]byte(`{"version":1}`)); err != nil {
		t.Fatalf("Restore old snapshot: %v", err)
	}
	if got := st.Selections(); len(got) != 0 {
		t.Fatalf("old snapshot selections = %v, want empty", got)
	}
	account, err := st.CreateAccount(model.CreateAccountInput{
		Label: "Snapshot Ops", Username: "snapshot", Password: "snapshot-password-not-a-credential",
		Platform: model.PlatformLinux, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := st.SetSelection(account.ID, "srv-a", "operator"); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	first, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(first, &document); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if _, ok := document["selections"]; !ok {
		t.Fatalf("snapshot has no selections field: %v", document)
	}
	restored := New()
	if err := restored.Restore(first); err != nil {
		t.Fatalf("Restore new snapshot: %v", err)
	}
	if selection, ok := restored.Selection(account.ID); !ok || selection.ServerID != "srv-a" {
		t.Fatalf("restored selection = %+v/%v", selection, ok)
	}
}

func TestCatalogSnapshotMapsAreCopies(t *testing.T) {
	st := New()
	if _, err := st.SetSelection("acc-01", "srv-a", "operator"); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	first, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if _, err := st.SetSelection("acc-01", "srv-b", "operator"); err != nil {
		t.Fatalf("SetSelection b: %v", err)
	}
	restored := New()
	if err := restored.Restore(first); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if selection, _ := restored.Selection("acc-01"); selection.ServerID != "srv-a" {
		t.Fatalf("restored selection = %q, want the earlier srv-a", selection.ServerID)
	}
	copied := restored.Selections()
	for id, selection := range copied {
		selection.ServerID = "mutated"
		copied[id] = selection
	}
	if selection, _ := restored.Selection("acc-01"); selection.ServerID != "srv-a" {
		t.Fatalf("Selections() aliased the store map")
	}
}
