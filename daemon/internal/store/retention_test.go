package store

import "testing"

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
