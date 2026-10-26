package runtime_test

import (
	"testing"

	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

// TestFlowMeterProgressStopsWhenTheStoreIsPoisoned pins the §2 flow contract:
// a chunk whose durable counter cannot be committed returns false so the gate
// closes the tunnel, and the store reports the poison.
func TestFlowMeterProgressStopsWhenTheStoreIsPoisoned(t *testing.T) {
	dir := t.TempDir()
	journal, err := store.OpenJournal(dir)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	st := store.NewEmpty()
	if _, err := journal.Recover(st); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	st.AttachJournal(journal)
	meter := runtime.NewFlowMeter(st)

	if !meter.Progress("", "acc-01", 0) {
		t.Fatal("zero-byte progress was refused")
	}
	if !meter.Progress("", "acc-01", 1024) {
		t.Fatal("healthy progress was refused")
	}
	// A closed journal is the deterministic form of a failed append.
	if err := journal.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if meter.Progress("", "acc-01", 1024) {
		t.Fatal("Progress accepted a chunk the journal could not commit")
	}
	if st.DurabilityError() == nil {
		t.Fatal("the store is not poisoned after a failed usage commit")
	}
}
