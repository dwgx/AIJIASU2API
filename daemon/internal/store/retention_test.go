package store

import (
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// TestPrependCapped covers the bounded-insert helper directly: newest first,
// oldest dropped at the cap, and a non-positive cap is a no-op.
func TestPrependCapped(t *testing.T) {
	var items []int
	for i := 1; i <= 5; i++ {
		items = prependCapped(items, i, 3)
	}
	want := []int{5, 4, 3}
	if len(items) != len(want) {
		t.Fatalf("items = %v, want %v", items, want)
	}
	for i := range want {
		if items[i] != want[i] {
			t.Fatalf("items[%d] = %d, want %d (all: %v)", i, items[i], want[i], items)
		}
	}

	if got := prependCapped([]int{1}, 2, 0); len(got) != 1 || got[0] != 1 {
		t.Fatalf("limit 0 changed the slice: %v", got)
	}
}

// TestAppendRequestStampsAndCaps proves the metered-row helper stamps a unique
// id and the store clock when the caller leaves them empty, keeps newest-first
// order, and never grows the request log past RequestRetention.
func TestAppendRequestStampsAndCaps(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	st := &Store{now: func() time.Time { return now }}

	first := st.AppendRequest(model.RequestLog{
		Source:       model.SourceGate,
		AccountLabel: "Northwind Ops",
		Target:       "example.com:443",
		Status:       200,
		Bytes:        8,
		LatencyMs:    3,
	})
	if first.ID == "" || first.TS != "2026-10-25T12:00:00Z" {
		t.Fatalf("stamped row = %+v, want a non-empty id and the store clock", first)
	}
	second := st.AppendRequest(model.RequestLog{ID: "caller-id", TS: "2026-10-24T00:00:00Z", Source: model.SourceGate})
	if second.ID != "caller-id" || second.TS != "2026-10-24T00:00:00Z" {
		t.Fatalf("caller identity was overwritten: %+v", second)
	}
	if first.ID == second.ID {
		t.Fatalf("generated ids collide: %q", first.ID)
	}
	rows := st.Requests()
	if len(rows) != 2 || rows[0].ID != "caller-id" || rows[1].ID != first.ID {
		t.Fatalf("requests = %+v, want newest first", rows)
	}

	// Fill the log to the cap, then append one more: the oldest row is dropped
	// and the length stays at RequestRetention.
	st.requests = make([]model.RequestLog, RequestRetention)
	for i := range st.requests {
		st.requests[i] = model.RequestLog{ID: "old", Source: model.SourceProxy}
	}
	st.AppendRequest(model.RequestLog{ID: "newest", Source: model.SourceGate})
	if len(st.requests) != RequestRetention {
		t.Fatalf("retained rows = %d, want %d", len(st.requests), RequestRetention)
	}
	if st.requests[0].ID != "newest" || st.requests[len(st.requests)-1].ID != "old" {
		t.Fatalf("cap order = first %q last %q", st.requests[0].ID, st.requests[len(st.requests)-1].ID)
	}
}
