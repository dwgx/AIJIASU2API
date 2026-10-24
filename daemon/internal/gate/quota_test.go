package gate

import (
	"testing"
)

// TestQuotaCheckRefusesBeforeOpenFlow pins the admission order: an over-budget
// account gets SOCKS5 0x02, the vendor session is never opened, and the flow
// record carries the quota_exceeded error class with zero bytes.
func TestQuotaCheckRefusesBeforeOpenFlow(t *testing.T) {
	session := &fakeSession{}
	calls := 0
	srv := startServer(t, Config{
		NodeID:    "acc-01:s1",
		AccountID: "acc-01",
		QuotaCheck: func(accountID string) (bool, int64, int64) {
			calls++
			if accountID != "acc-01" {
				t.Errorf("QuotaCheck accountID = %q, want acc-01", accountID)
			}
			return false, 120, 100
		},
	}, session)

	client := dialGate(t, srv)
	greet(t, client, authNone)
	connectRequest(t, client, cmdConnect, atypDomain, domainAddr("example.com"), 443)
	if rep := readReply(t, client); rep != repNotAllowed {
		t.Fatalf("reply = %#x, want 0x02 (not allowed by ruleset)", rep)
	}

	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	record := records[0]
	if record.Status != FlowStatusRejected {
		t.Fatalf("status = %q, want %q", record.Status, FlowStatusRejected)
	}
	if record.ErrorClass != FlowErrorQuota {
		t.Fatalf("errorClass = %q, want %q", record.ErrorClass, FlowErrorQuota)
	}
	if record.BytesIn != 0 || record.BytesOut != 0 {
		t.Fatalf("refused flow bytes = %d/%d, want 0/0", record.BytesIn, record.BytesOut)
	}
	if opens := session.recorded(); len(opens) != 0 {
		t.Fatalf("OpenFlow calls = %v, want none", opens)
	}
	if calls != 1 {
		t.Fatalf("QuotaCheck calls = %d, want 1", calls)
	}
}

// TestQuotaCheckAllowsUnderBudget proves an admitted account relays normally.
func TestQuotaCheckAllowsUnderBudget(t *testing.T) {
	session := &fakeSession{}
	srv := startServer(t, Config{
		NodeID:    "acc-01:s1",
		AccountID: "acc-01",
		QuotaCheck: func(string) (bool, int64, int64) {
			return true, 40, 100
		},
	}, session)

	client := dialGate(t, srv)
	greet(t, client, authNone)
	connectRequest(t, client, cmdConnect, atypDomain, domainAddr("example.com"), 443)
	if rep := readReply(t, client); rep != repSucceeded {
		t.Fatalf("reply = %#x, want success", rep)
	}
	echoOnce(t, client, []byte("quota"))
	_ = client.Close()

	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool {
		return len(records) == 1 && records[0].Status == FlowStatusOK
	})
	if records[0].ErrorClass != "" {
		t.Fatalf("admitted flow errorClass = %q, want empty", records[0].ErrorClass)
	}
	if opens := session.recorded(); len(opens) != 1 {
		t.Fatalf("OpenFlow calls = %v, want one", opens)
	}
}
