package store

import (
	"errors"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// TestCreateLeaseRefusesBeyondAdmissionSlots pins the default: one account
// backs one active lease, even when its manual Concurrency and node count
// would allow more. The refusal is a quota-class 429 with the dedicated
// account_slots_exhausted reason.
func TestCreateLeaseRefusesBeyondAdmissionSlots(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 8)
	st := fixture.st
	if _, err := st.SetAccountUseCount(fixture.account.ID, 1); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}

	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid,
	}); err != nil {
		t.Fatalf("first CreateLease: %v", err)
	}
	second := addLeaseUser(t, st, "second-bot", nil)
	_, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: second.ID, Platform: model.PlatformAndroid,
	})
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) || !errors.Is(err, ErrQuota) || leaseErr.Reason != "account_slots_exhausted" {
		t.Fatalf("second create = %v, want account_slots_exhausted", err)
	}
	if got := len(st.Leases()); got != 1 {
		t.Fatalf("leases after refusal = %d, want 1", got)
	}
}

// TestCreateLeaseAdmissionStaysOneDespiteDisplayCapacity proves the split
// introduced by AJS-332: UseCount=3 and AJS_ACCOUNT_SLOTS=2 give the UI a
// display capacity of two, but the lease guard admits exactly one active
// lease because the multi-slot runtime is not implemented.
func TestCreateLeaseAdmissionStaysOneDespiteDisplayCapacity(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 8)
	t.Setenv("AJS_ACCOUNT_SLOTS", "2")
	st := fixture.st
	if _, err := st.SetAccountUseCount(fixture.account.ID, 3); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}
	if got := ConfiguredSlotCapacity(3); got != 2 {
		t.Fatalf("display capacity = %d, want 2", got)
	}
	if got := ConfiguredAdmissionSlots(3); got != MaxAdmissionSlots {
		t.Fatalf("admission capacity = %d, want %d", got, MaxAdmissionSlots)
	}

	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid,
	}); err != nil {
		t.Fatalf("first CreateLease: %v", err)
	}
	second := addLeaseUser(t, st, "second-bot", nil)
	_, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: second.ID, Platform: model.PlatformAndroid,
	})
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) || leaseErr.Reason != "account_slots_exhausted" {
		t.Fatalf("second create = %v, want account_slots_exhausted", err)
	}
	if got := len(st.Leases()); got != 1 {
		t.Fatalf("leases = %d, want 1", got)
	}
}

// TestCreateLeaseSlotsIgnoreInvalidOperatorCap proves a typo in
// AJS_ACCOUNT_SLOTS falls back to the safe default instead of widening the
// cap. The display helper and the admission guard both stay at one.
func TestCreateLeaseSlotsIgnoreInvalidOperatorCap(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 8)
	t.Setenv("AJS_ACCOUNT_SLOTS", "not-a-number")
	st := fixture.st
	if _, err := st.SetAccountUseCount(fixture.account.ID, 8); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}
	if got := ConfiguredSlotCapacity(8); got != 1 {
		t.Fatalf("display capacity = %d, want 1", got)
	}

	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: fixture.user.ID, Platform: model.PlatformAndroid,
	}); err != nil {
		t.Fatalf("first CreateLease: %v", err)
	}
	second := addLeaseUser(t, st, "second-bot", nil)
	_, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: second.ID, Platform: model.PlatformAndroid,
	})
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) || leaseErr.Reason != "account_slots_exhausted" {
		t.Fatalf("second create = %v, want account_slots_exhausted", err)
	}
}

// TestSetAccountUseCountClamps pins the stored range and the default for a
// missing vendor field.
func TestSetAccountUseCountClamps(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 8)
	st := fixture.st

	for _, tc := range []struct {
		in   int
		want int
	}{
		{in: 0, want: 1},
		{in: -4, want: 1},
		{in: 7, want: 7},
		{in: 5000, want: 2000},
	} {
		got, err := st.SetAccountUseCount(fixture.account.ID, tc.in)
		if err != nil {
			t.Fatalf("SetAccountUseCount(%d): %v", tc.in, err)
		}
		if got.UseCount != tc.want {
			t.Fatalf("SetAccountUseCount(%d) = %d, want %d", tc.in, got.UseCount, tc.want)
		}
	}
}

// TestCreateLeaseAdmissionIgnoresUseCountAboveOne proves the admission guard
// does not follow the vendor's UseCount: a generous AJS_ACCOUNT_SLOTS and
// UseCount=2 still admit only the first lease.
func TestCreateLeaseAdmissionIgnoresUseCountAboveOne(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 8)
	t.Setenv("AJS_ACCOUNT_SLOTS", "5")
	st := fixture.st
	if _, err := st.SetAccountUseCount(fixture.account.ID, 2); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}
	if got := ConfiguredSlotCapacity(2); got != 2 {
		t.Fatalf("display capacity = %d, want 2", got)
	}

	first := fixture.user
	second := addLeaseUser(t, st, "second-bot", nil)
	if _, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: first.ID, Platform: model.PlatformAndroid,
	}); err != nil {
		t.Fatalf("CreateLease(%s): %v", first.Name, err)
	}
	_, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: second.ID, Platform: model.PlatformAndroid,
	})
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) || leaseErr.Reason != "account_slots_exhausted" {
		t.Fatalf("second create = %v, want account_slots_exhausted", err)
	}
	if got := len(st.Leases()); got != 1 {
		t.Fatalf("leases = %d, want 1", got)
	}
}

// TestCreateLeaseAdmissionCannotBeBypassedByEnv runs the whole operator-cap
// range against a UseCount=8 account. Every value must leave the admission
// guard at exactly one active lease; only the display capacity changes.
func TestCreateLeaseAdmissionCannotBeBypassedByEnv(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured string
		display    int
	}{
		{name: "default", configured: "", display: 1},
		{name: "one", configured: "1", display: 1},
		{name: "two", configured: "2", display: 2},
		{name: "eight", configured: "8", display: 8},
		{name: "vendor maximum", configured: "2000", display: 8},
		{name: "invalid falls back to one", configured: "bogus", display: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
			fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 8)
			t.Setenv("AJS_ACCOUNT_SLOTS", tc.configured)
			st := fixture.st
			if _, err := st.SetAccountUseCount(fixture.account.ID, 8); err != nil {
				t.Fatalf("SetAccountUseCount: %v", err)
			}
			if got := ConfiguredSlotCapacity(8); got != tc.display {
				t.Fatalf("display capacity = %d, want %d", got, tc.display)
			}
			if got := ConfiguredAdmissionSlots(8); got != MaxAdmissionSlots {
				t.Fatalf("admission capacity = %d, want %d", got, MaxAdmissionSlots)
			}

			if _, _, err := st.CreateLease(model.CreateLeaseInput{
				UserID: fixture.user.ID, Platform: model.PlatformAndroid,
			}); err != nil {
				t.Fatalf("first CreateLease: %v", err)
			}
			second := addLeaseUser(t, st, "second-bot", nil)
			_, _, err := st.CreateLease(model.CreateLeaseInput{
				UserID: second.ID, Platform: model.PlatformAndroid,
			})
			var leaseErr *LeaseError
			if !errors.As(err, &leaseErr) || leaseErr.Reason != "account_slots_exhausted" {
				t.Fatalf("second create = %v, want account_slots_exhausted", err)
			}
			if got := len(st.Leases()); got != 1 {
				t.Fatalf("leases = %d, want 1", got)
			}
		})
	}
}
