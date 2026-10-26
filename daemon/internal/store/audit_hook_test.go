package store

import (
	"sync"
	"testing"

	"ajsaccountd/internal/model"
)

// TestAuditHookFiresOnlyForCriticalRows pins the audit.critical contract: the
// hook sees a critical row with its id/action/actor/target and meta, and every
// lower severity stays local to the store.
func TestAuditHookFiresOnlyForCriticalRows(t *testing.T) {
	st := NewEmpty()
	var mu sync.Mutex
	var received []model.AuditEvent
	st.SetAuditHook(func(ev model.AuditEvent) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, ev)
	})

	for _, severity := range []model.Severity{
		model.SeverityInfo, model.SeverityWarn, model.SeverityError,
	} {
		st.RecordAudit(severity, "operator", "device.retire", "dev_1", "reason=cleanup")
	}
	if len(received) != 0 {
		t.Fatalf("hook fired for non-critical rows: %+v", received)
	}

	auditID, err := st.WriteAudit(model.SeverityCritical, "operator", "secret.rotate", "key-1",
		"master key rotated", map[string]string{"scope": "secrets"})
	if err != nil {
		t.Fatalf("WriteAudit: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("hook received %d critical rows, want 1", len(received))
	}
	got := received[0]
	if got.ID != auditID || got.Action != "secret.rotate" || got.Actor != "operator" ||
		got.Target != "key-1" || got.Severity != model.SeverityCritical {
		t.Fatalf("critical event = %+v, want id=%s action=secret.rotate", got, auditID)
	}
	if got.Meta["scope"] != "secrets" {
		t.Fatalf("meta = %v, want scope=secrets", got.Meta)
	}

	// The same row is persisted for the audit read/export routes.
	rows := st.Audit(1)
	if len(rows) != 1 || rows[0].ID != auditID {
		t.Fatalf("Audit(1) = %+v, want the critical row", rows)
	}

	// Clearing the hook stops delivery without touching the history.
	st.SetAuditHook(nil)
	st.RecordAudit(model.SeverityCritical, "operator", "secret.rotate", "key-2", "again")
	if len(received) != 1 {
		t.Fatalf("hook fired after being cleared: %+v", received)
	}
	if rows := st.Audit(1); len(rows) != 1 || rows[0].Target != "key-2" {
		t.Fatalf("cleared hook dropped the audit row: %+v", rows)
	}
}

// TestAuditHookNilByDefault keeps the zero-configuration store quiet.
func TestAuditHookNilByDefault(t *testing.T) {
	st := NewEmpty()
	st.RecordAudit(model.SeverityCritical, "operator", "secret.rotate", "key-1", "rotation")
	if rows := st.Audit(1); len(rows) != 1 {
		t.Fatalf("audit rows = %+v, want the critical row retained", rows)
	}
}
