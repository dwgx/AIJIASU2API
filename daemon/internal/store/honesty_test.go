package store_test

import (
	"strings"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// TestProbeNodeAuditIsHonest locks the first-slice honesty fix: the store-only
// probe moves a timestamp and must not claim it verified egress.
func TestProbeNodeAuditIsHonest(t *testing.T) {
	st := store.New()
	var nodeID string
	for _, node := range st.Nodes() {
		if node.State != model.NodeDown {
			nodeID = node.ID
			break
		}
	}
	if nodeID == "" {
		t.Fatal("seeded store has no probeable node")
	}
	if _, err := st.ProbeNode(nodeID); err != nil {
		t.Fatalf("ProbeNode: %v", err)
	}
	events := st.Audit(1)
	if len(events) != 1 {
		t.Fatalf("audit = %+v, want one event", events)
	}
	event := events[0]
	if event.Action != "node.probe" || event.Target == "" {
		t.Fatalf("audit event = %+v", event)
	}
	if strings.Contains(event.Detail, "egress verified") || strings.Contains(event.Detail, "Canary ok") {
		t.Fatalf("probe audit still claims verification: %q", event.Detail)
	}
	if !strings.Contains(event.Detail, "no upstream probe performed") {
		t.Fatalf("probe audit detail = %q, want the no-probe wording", event.Detail)
	}
}
