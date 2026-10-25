package gate

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
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

// quotaSinkSession accepts an admitted flow and discards everything the client
// sends. It never echoes, so only the client -> target direction reports
// progress and a test can pin the chunk accounting exactly.
type quotaSinkSession struct{}

func (quotaSinkSession) OpenFlow(ctx context.Context, host string, port int) (net.Conn, error) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.Copy(io.Discard, server)
	}()
	return client, nil
}

// TestMeterProgressClosesTunnel pins the SOCKS5 in-flight path: the relay
// reports bytes to the meter, the report that crosses the budget closes the
// tunnel, and the finished flow records quota_exceeded. TCP reads are unaligned,
// so the first report can already cross the budget and be refused; the test
// requires at least one report and that the reported bytes equal the recorded
// ingress bytes, proving the refused flow is charged exactly once.
func TestMeterProgressClosesTunnel(t *testing.T) {
	var calls, total atomic.Int64
	srv := startServer(t, Config{
		NodeID:    "acc-01:s1",
		AccountID: "acc-01",
		MeterProgress: func(userID, accountID string, n int64) bool {
			if userID != "" {
				t.Errorf("loopback userID = %q, want empty", userID)
			}
			if accountID != "acc-01" {
				t.Errorf("accountID = %q, want acc-01", accountID)
			}
			calls.Add(1)
			return total.Add(n) <= int64(ProgressChunk)
		},
	}, quotaSinkSession{})

	client := dialGate(t, srv)
	greet(t, client, authNone)
	connectRequest(t, client, cmdConnect, atypDomain, domainAddr("example.com"), 443)
	if rep := readReply(t, client); rep != repSucceeded {
		t.Fatalf("reply = %#x, want success", rep)
	}

	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = client.Write(make([]byte, 2*ProgressChunk))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("tunnel stayed open after the meter refused the flow")
	}
	_ = client.Close()

	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	record := records[0]
	if record.Status != FlowStatusOK {
		t.Fatalf("status = %q, want %q (the flow reached relay)", record.Status, FlowStatusOK)
	}
	if record.ErrorClass != FlowErrorQuota {
		t.Fatalf("errorClass = %q, want %q", record.ErrorClass, FlowErrorQuota)
	}
	if record.BytesIn < int64(ProgressChunk) {
		t.Fatalf("ingress bytes = %d, want at least one chunk (%d)", record.BytesIn, ProgressChunk)
	}
	if got := calls.Load(); got < 1 {
		t.Fatalf("progress calls = %d, want calls >= 1", got)
	}
	if got, in := total.Load(), record.BytesIn; got != in {
		t.Fatalf("meter charged %d bytes, record.BytesIn = %d, want equal (each byte charged once)", got, in)
	}
}

// TestHTTPMeterProgressClosesTunnel pins the HTTP CONNECT path to the same
// meter: the tunnel is closed after the chunk that exhausts the budget and the
// flow record carries quota_exceeded.
func TestHTTPMeterProgressClosesTunnel(t *testing.T) {
	var total atomic.Int64
	srv := startHTTPGate(t, Config{
		NodeID:    "acc-01:s1",
		AccountID: "acc-01",
		MeterProgress: func(userID, accountID string, n int64) bool {
			if userID != "" {
				t.Errorf("loopback userID = %q, want empty", userID)
			}
			if accountID != "acc-01" {
				t.Errorf("accountID = %q, want acc-01", accountID)
			}
			return total.Add(n) <= int64(ProgressChunk)
		},
	}, quotaSinkSession{})

	conn, reader := dialHTTPGate(t, srv)
	writeConnect(t, conn, "example.com:443", "")
	if status, _ := readHTTPHead(t, reader); status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}

	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = conn.Write(make([]byte, 2*ProgressChunk))
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("HTTP tunnel stayed open after the meter refused the flow")
	} else if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
		t.Fatal("HTTP tunnel stayed open until the test deadline")
	}
	_ = conn.Close()

	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	record := records[0]
	if record.Status != FlowStatusOK || record.ErrorClass != FlowErrorQuota {
		t.Fatalf("record = %q/%q, want ok/%q", record.Status, record.ErrorClass, FlowErrorQuota)
	}
}
