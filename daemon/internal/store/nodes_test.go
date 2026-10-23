package store_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// runtimeNode is a valid controller-written node for accountID.
func runtimeNode(accountID string) model.ProxyNode {
	return model.ProxyNode{
		ID:        accountID + ":s1",
		AccountID: accountID,
		Label:     "cn-" + accountID + "-s1",
		Region:    "cn",
		Country:   "CN",
		City:      "Shanghai",
		State:     model.NodeLive,
		Protocol:  model.ProtocolSOCKS5,
		GatePort:  13000,
		ServerID:  "s1",
		Capacity:  32,
	}
}

func TestPutNodeCreatesAndUpdates(t *testing.T) {
	st := store.New()
	account, err := st.CreateAccount(accountInput("runtime-nodes", "runtime-password-not-a-credential"))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	created, err := st.PutNode(runtimeNode(account.ID))
	if err != nil {
		t.Fatalf("PutNode create: %v", err)
	}
	if created.AccountLabel != account.Label {
		t.Fatalf("AccountLabel = %q, want the account label %q", created.AccountLabel, account.Label)
	}
	if created.ServerID != "s1" || created.GatePort != 13000 || created.State != model.NodeLive {
		t.Fatalf("created node = %+v, want server s1 on 13000 LIVE", created)
	}
	if created.LastProbeAt == "" {
		t.Fatal("created node has no LastProbeAt")
	}
	if created.Capacity != 32 || created.Protocol != model.ProtocolSOCKS5 {
		t.Fatalf("created node = %+v, want capacity 32 socks5", created)
	}

	stored, ok := st.Node(created.ID)
	if !ok {
		t.Fatalf("Node(%q): missing after PutNode", created.ID)
	}
	if stored != created {
		t.Fatalf("stored node = %+v, want %+v", stored, created)
	}
	if got := st.Nodes(); len(got) != 13 {
		t.Fatalf("nodes = %d, want the 12 seeds plus the new node", len(got))
	}
	accountAfter, ok := st.Account(account.ID)
	if !ok {
		t.Fatalf("account %q disappeared", account.ID)
	}
	if accountAfter.NodeCount != 1 || accountAfter.LiveNodeCount != 1 {
		t.Fatalf("account counts = %d/%d, want 1/1", accountAfter.NodeCount, accountAfter.LiveNodeCount)
	}

	// A second write updates the runtime fields and preserves the probe and
	// lease fields the caller left zero.
	stored.InUse = 4
	stored.SuccessRate = 98.5
	if _, err := st.PutNode(stored); err != nil {
		t.Fatalf("PutNode in-use update: %v", err)
	}
	update := runtimeNode(account.ID)
	update.State = model.NodeDraining
	update.GatePort = 13001
	update.EgressIP = "198.51.100.7"
	updated, err := st.PutNode(update)
	if err != nil {
		t.Fatalf("PutNode update: %v", err)
	}
	if updated.State != model.NodeDraining || updated.GatePort != 13001 || updated.EgressIP != "198.51.100.7" {
		t.Fatalf("updated node = %+v, want DRAINING on 13001 with the egress IP", updated)
	}
	if updated.InUse != 4 || updated.SuccessRate != 98.5 {
		t.Fatalf("runtime update lost probe/lease fields: %+v", updated)
	}
	if updated.LastProbeAt != created.LastProbeAt {
		t.Fatalf("LastProbeAt changed from %q to %q", created.LastProbeAt, updated.LastProbeAt)
	}
	if got := st.Nodes(); len(got) != 13 {
		t.Fatalf("nodes = %d after update, want 13 (no duplicate)", len(got))
	}
}

func TestPutNodeValidation(t *testing.T) {
	st := store.New()
	account, err := st.CreateAccount(accountInput("runtime-validation", "validation-password-not-a-credential"))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	valid := runtimeNode(account.ID)

	cases := []struct {
		name   string
		mutate func(node *model.ProxyNode)
	}{
		{name: "empty id", mutate: func(n *model.ProxyNode) { n.ID = "" }},
		{name: "id with space", mutate: func(n *model.ProxyNode) { n.ID = "acc :s1" }},
		{name: "unknown account", mutate: func(n *model.ProxyNode) { n.AccountID = "acc-missing" }},
		{name: "empty label", mutate: func(n *model.ProxyNode) { n.Label = "" }},
		{name: "uppercase label", mutate: func(n *model.ProxyNode) { n.Label = "CN-ACC-01" }},
		{name: "double hyphen label", mutate: func(n *model.ProxyNode) { n.Label = "cn--s1" }},
		{name: "empty region", mutate: func(n *model.ProxyNode) { n.Region = " " }},
		{name: "long region", mutate: func(n *model.ProxyNode) { n.Region = strings.Repeat("x", 65) }},
		{name: "port too high", mutate: func(n *model.ProxyNode) { n.GatePort = 65536 }},
		{name: "negative port", mutate: func(n *model.ProxyNode) { n.GatePort = -1 }},
		{name: "unknown state", mutate: func(n *model.ProxyNode) { n.State = "BROKEN" }},
		{name: "negative inUse", mutate: func(n *model.ProxyNode) { n.InUse = -1 }},
		{name: "negative capacity", mutate: func(n *model.ProxyNode) { n.Capacity = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := valid
			tc.mutate(&node)
			if _, err := st.PutNode(node); !errors.Is(err, store.ErrInvalid) && !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("PutNode error = %v, want ErrInvalid or ErrNotFound", err)
			}
		})
	}

	if _, err := st.PutNode(valid); err != nil {
		t.Fatalf("PutNode valid: %v", err)
	}
	duplicate := valid
	duplicate.ID = account.ID + ":s2"
	if _, err := st.PutNode(duplicate); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate label error = %v, want ErrConflict", err)
	}
}

func TestDeleteNodeRemovesLeases(t *testing.T) {
	st := store.New()
	if _, ok := st.Lease("lease-01"); !ok {
		t.Fatal("seed lease-01 is missing")
	}
	if err := st.DeleteNode("node-01"); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	if _, ok := st.Node("node-01"); ok {
		t.Fatal("node-01 survived DeleteNode")
	}
	if _, ok := st.Lease("lease-01"); ok {
		t.Fatal("lease-01 survived DeleteNode of its node")
	}
	if got := len(st.Nodes()); got != 11 {
		t.Fatalf("nodes = %d, want 11", got)
	}
	account, ok := st.Account("acc-01")
	if !ok {
		t.Fatal("acc-01 disappeared")
	}
	if account.NodeCount != 2 || account.LiveNodeCount != 1 {
		t.Fatalf("acc-01 counts = %d/%d, want 2/1", account.NodeCount, account.LiveNodeCount)
	}
	if err := st.DeleteNode("node-01"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second DeleteNode error = %v, want ErrNotFound", err)
	}
}

func TestNodeServerIDSnapshot(t *testing.T) {
	st := store.New()
	account, err := st.CreateAccount(accountInput("runtime-snapshot", "snapshot-password-not-a-credential"))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	node := runtimeNode(account.ID)
	created, err := st.PutNode(node)
	if err != nil {
		t.Fatalf("PutNode: %v", err)
	}

	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !strings.Contains(string(data), `"serverId": "s1"`) {
		t.Fatalf("snapshot does not carry serverId:\n%s", data)
	}
	var decoded struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	found := false
	for _, row := range decoded.Nodes {
		if row["id"] == node.ID {
			found = true
			if row["serverId"] != "s1" {
				t.Fatalf("snapshot node serverId = %v, want s1", row["serverId"])
			}
		}
	}
	if !found {
		t.Fatalf("snapshot has no node %q", node.ID)
	}

	restored := store.New()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	after, ok := restored.Node(node.ID)
	if !ok {
		t.Fatalf("restored store has no node %q", node.ID)
	}
	if after != created {
		t.Fatalf("restored node = %+v, want %+v", after, created)
	}
	// The seeded nodes are still valid rows in the same document.
	if _, ok := restored.Node("node-01"); !ok {
		t.Fatal("seed node-01 missing after restore")
	}
}
