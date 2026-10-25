package store

import (
	"errors"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// TestCreateLeaseRefusesBeyondEffectiveSlots pins the default: one account
// backs one active lease, even when its manual Concurrency and node count
// would allow more. The refusal is a quota-class 429 with the dedicated
// account_slots_exhausted reason.
func TestCreateLeaseRefusesBeyondEffectiveSlots(t *testing.T) {
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

// TestCreateLeaseAllowsOperatorCapUpToUseCount proves the guard matrix: with
// UseCount=3 and AJS_ACCOUNT_SLOTS=2 exactly two leases are allowed, and the
// third is refused even before node capacity is considered.
func TestCreateLeaseAllowsOperatorCapUpToUseCount(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 8)
	t.Setenv("AJS_ACCOUNT_SLOTS", "2")
	st := fixture.st
	if _, err := st.SetAccountUseCount(fixture.account.ID, 3); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}

	first := fixture.user
	second := addLeaseUser(t, st, "second-bot", nil)
	third := addLeaseUser(t, st, "third-bot", nil)
	for _, user := range []model.PanelUser{first, second} {
		if _, _, err := st.CreateLease(model.CreateLeaseInput{
			UserID: user.ID, Platform: model.PlatformAndroid,
		}); err != nil {
			t.Fatalf("CreateLease(%s): %v", user.Name, err)
		}
	}
	_, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: third.ID, Platform: model.PlatformAndroid,
	})
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) || leaseErr.Reason != "account_slots_exhausted" {
		t.Fatalf("third create = %v, want account_slots_exhausted", err)
	}
	if got := len(st.Leases()); got != 2 {
		t.Fatalf("leases = %d, want 2", got)
	}
}

// TestCreateLeaseSlotsIgnoreInvalidOperatorCap proves a typo in
// AJS_ACCOUNT_SLOTS falls back to the safe default instead of widening the
// cap.
func TestCreateLeaseSlotsIgnoreInvalidOperatorCap(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 8)
	t.Setenv("AJS_ACCOUNT_SLOTS", "not-a-number")
	st := fixture.st
	if _, err := st.SetAccountUseCount(fixture.account.ID, 8); err != nil {
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

// TestCreateLeaseUseCountCapsOperatorLimit proves the min() in the guard: a
// generous AJS_ACCOUNT_SLOTS cannot exceed the vendor's purchased UseCount.
func TestCreateLeaseUseCountCapsOperatorLimit(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 8)
	t.Setenv("AJS_ACCOUNT_SLOTS", "5")
	st := fixture.st
	if _, err := st.SetAccountUseCount(fixture.account.ID, 2); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}

	first := fixture.user
	second := addLeaseUser(t, st, "second-bot", nil)
	third := addLeaseUser(t, st, "third-bot", nil)
	for _, user := range []model.PanelUser{first, second} {
		if _, _, err := st.CreateLease(model.CreateLeaseInput{
			UserID: user.ID, Platform: model.PlatformAndroid,
		}); err != nil {
			t.Fatalf("CreateLease(%s): %v", user.Name, err)
		}
	}
	_, _, err := st.CreateLease(model.CreateLeaseInput{
		UserID: third.ID, Platform: model.PlatformAndroid,
	})
	var leaseErr *LeaseError
	if !errors.As(err, &leaseErr) || leaseErr.Reason != "account_slots_exhausted" {
		t.Fatalf("third create = %v, want account_slots_exhausted", err)
	}
}
