package gate

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFlowRingCapacityAndDrop(t *testing.T) {
	ring := newFlowRing(3)
	base := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= 5; i++ {
		ring.add(FlowRecord{
			ID:        strconv.Itoa(i),
			StartedAt: base.Add(time.Duration(i) * time.Second),
		})
	}

	all := ring.snapshot(0)
	if len(all) != 3 {
		t.Fatalf("ring size = %d, want 3", len(all))
	}
	for i, want := range []string{"5", "4", "3"} {
		if all[i].ID != want {
			t.Fatalf("record[%d] = %q, want %q (newest first)", i, all[i].ID, want)
		}
	}
	if dropped := ring.droppedCount(); dropped != 2 {
		t.Fatalf("dropped = %d, want 2", dropped)
	}

	limited := ring.snapshot(2)
	if len(limited) != 2 || limited[0].ID != "5" || limited[1].ID != "4" {
		t.Fatalf("limit 2 = %+v, want [5 4]", limited)
	}
	if wide := ring.snapshot(10); len(wide) != 3 {
		t.Fatalf("limit 10 returned %d records, want the ring's 3", len(wide))
	}
}

// domainAddr builds the ATYP 3 address field for a domain name.
func domainAddr(host string) []byte {
	return append([]byte{byte(len(host))}, host...)
}

func waitFlowRecords(t *testing.T, srv *Server, cond func([]FlowRecord) bool) []FlowRecord {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		records := srv.Flows(0)
		if cond(records) {
			return records
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for flow records; last = %+v", records)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestFlowRingRecordsRejectedFlow covers a refusal after the CONNECT frame:
// the record names the host-only target and the draining seam.
func TestFlowRingRecordsRejectedFlow(t *testing.T) {
	srv := startServer(t, Config{NodeID: "acc-01:s1", AccountID: "acc-01"}, &fakeSession{})
	if err := srv.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	client := dialGate(t, srv)
	greet(t, client, authNone)
	connectRequest(t, client, cmdConnect, atypDomain, domainAddr("example.com"), 443)
	if rep := readReply(t, client); rep != repConnectionRefused {
		t.Fatalf("reply = %#x, want connection refused", rep)
	}

	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	record := records[0]
	if record.Status != FlowStatusRejected || record.ErrorClass != FlowErrorDraining {
		t.Fatalf("record = %+v, want rejected/draining", record)
	}
	if record.Method != "CONNECT" || record.TargetHost != "example.com" || record.TargetPort != 443 {
		t.Fatalf("target = %s:%d method=%q, want CONNECT example.com:443", record.TargetHost, record.TargetPort, record.Method)
	}
	if record.NodeID != "" || record.AccountID != "" {
		t.Fatalf("gate stamped node/account ids; the runtime owns the join: %+v", record)
	}
	if record.ID == "" || record.StartedAt.IsZero() {
		t.Fatalf("record lacks id/startedAt: %+v", record)
	}
	if strings.Contains(record.TargetHost, ":") || strings.Contains(record.TargetHost, "/") {
		t.Fatalf("target host is not host-only: %q", record.TargetHost)
	}
}

func TestFlowRingRecordsHandshakeReject(t *testing.T) {
	srv := startServer(t, Config{NodeID: "acc-01:s1"}, &fakeSession{})

	client := dialGate(t, srv)
	if _, err := client.Write([]byte{0x04, 0x01, 0x00}); err != nil {
		t.Fatalf("write bad greeting: %v", err)
	}

	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	record := records[0]
	if record.Status != FlowStatusRejected || record.ErrorClass != FlowErrorHandshake {
		t.Fatalf("record = %+v, want rejected/handshake", record)
	}
	if record.TargetHost != "" || record.TargetPort != 0 || record.Method != "" {
		t.Fatalf("handshake reject invented a target: %+v", record)
	}
}

func TestFlowRingRecordsOpenFlowFailure(t *testing.T) {
	session := &fakeSession{err: errors.New("vendor rejected")}
	srv := startServer(t, Config{NodeID: "acc-01:s1"}, session)

	client := dialGate(t, srv)
	greet(t, client, authNone)
	connectRequest(t, client, cmdConnect, atypDomain, domainAddr("example.com"), 8443)
	if rep := readReply(t, client); rep != repConnectionRefused {
		t.Fatalf("reply = %#x, want connection refused", rep)
	}

	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool { return len(records) == 1 })
	record := records[0]
	if record.Status != FlowStatusRejected || record.ErrorClass != FlowErrorOpenFlow {
		t.Fatalf("record = %+v, want rejected/open_flow", record)
	}
	if record.TargetHost != "example.com" || record.TargetPort != 8443 {
		t.Fatalf("target = %s:%d, want example.com:8443", record.TargetHost, record.TargetPort)
	}
}

// TestFlowRingRecordsAcceptedFlow proves a relayed flow records the host-only
// target, byte counts and the ok status.
func TestFlowRingRecordsAcceptedFlow(t *testing.T) {
	srv := startServer(t, Config{NodeID: "acc-01:s1"}, &fakeSession{})

	client := dialGate(t, srv)
	connectOK(t, client, atypDomain, domainAddr("example.com"), 443)
	echoOnce(t, client, []byte("hello"))
	if err := client.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}

	records := waitFlowRecords(t, srv, func(records []FlowRecord) bool {
		return len(records) == 1 && records[0].Status == FlowStatusOK
	})
	record := records[0]
	if record.TargetHost != "example.com" || record.TargetPort != 443 || record.Method != "CONNECT" {
		t.Fatalf("target = %s:%d method=%q, want CONNECT example.com:443", record.TargetHost, record.TargetPort, record.Method)
	}
	if record.BytesIn != int64(len("hello")) || record.BytesOut != int64(len("hello")) {
		t.Fatalf("bytes = %d/%d, want 5/5", record.BytesIn, record.BytesOut)
	}
	if record.ErrorClass != "" {
		t.Fatalf("ok flow carries an error class: %q", record.ErrorClass)
	}
	if !strings.HasPrefix(record.ID, "acc-01:s1#") {
		t.Fatalf("id = %q, want the node prefix", record.ID)
	}
	if srv.Dropped() != 0 {
		t.Fatalf("dropped = %d, want 0 under capacity", srv.Dropped())
	}
}
