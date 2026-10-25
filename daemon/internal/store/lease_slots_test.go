package store

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// TestCreateLeaseRefusesBeyondCapacity pins the default: one account backs one
// active lease while its UseCount and AJS_ACCOUNT_SLOTS allow one slot. The
// refusal is a quota-class 429 with the dedicated account_slots_exhausted
// reason.
func TestCreateLeaseRefusesBeyondCapacity(t *testing.T) {
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

// TestCreateLeaseAdmitsConfiguredSlotCapacity proves the lease guard follows
// the slot allowance: UseCount=3 with AJS_ACCOUNT_SLOTS=2 admits two leases and
// refuses the third with account_slots_exhausted.
func TestCreateLeaseAdmitsConfiguredSlotCapacity(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 8)
	t.Setenv("AJS_ACCOUNT_SLOTS", "2")
	st := fixture.st
	account, err := st.SetAccountUseCount(fixture.account.ID, 3)
	if err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}
	if got := ConfiguredSlotCapacity(3); got != 2 {
		t.Fatalf("display capacity = %d, want 2", got)
	}
	if got := st.accountCapacityLocked(account, now); got != 2 {
		t.Fatalf("admission capacity = %d, want 2", got)
	}

	users := []model.PanelUser{fixture.user, addLeaseUser(t, st, "second-bot", nil)}
	for i, user := range users {
		if _, _, err := st.CreateLease(model.CreateLeaseInput{
			UserID: user.ID, Platform: model.PlatformAndroid,
		}); err != nil {
			t.Fatalf("CreateLease #%d: %v", i+1, err)
		}
	}
	third := addLeaseUser(t, st, "third-bot", nil)
	_, _, err = st.CreateLease(model.CreateLeaseInput{
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

// TestCreateLeaseAdmissionFollowsSlotCapacity proves the guard follows the
// vendor UseCount capped by AJS_ACCOUNT_SLOTS: UseCount=2 with
// AJS_ACCOUNT_SLOTS=5 admits two leases, then refuses the third.
func TestCreateLeaseAdmissionFollowsSlotCapacity(t *testing.T) {
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

	users := []model.PanelUser{fixture.user, addLeaseUser(t, st, "second-bot", nil)}
	for i, user := range users {
		if _, _, err := st.CreateLease(model.CreateLeaseInput{
			UserID: user.ID, Platform: model.PlatformAndroid,
		}); err != nil {
			t.Fatalf("CreateLease #%d: %v", i+1, err)
		}
	}
	third := addLeaseUser(t, st, "third-bot", nil)
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

// TestCreateLeaseAdmissionFollowsEnvCapacity runs the whole operator-cap range
// against a UseCount=8 account with enough nodes to serve it. The admitted
// lease count equals the configured slot capacity; only the display capacity
// changes with the value.
func TestCreateLeaseAdmissionFollowsEnvCapacity(t *testing.T) {
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
			fixture := newLeaseFixture(t, &now, model.PlatformAndroid, 32)
			t.Setenv("AJS_ACCOUNT_SLOTS", tc.configured)
			st := fixture.st
			account, err := st.SetAccountUseCount(fixture.account.ID, 8)
			if err != nil {
				t.Fatalf("SetAccountUseCount: %v", err)
			}
			for index := 3; index <= 8; index++ {
				addLeaseNode(t, st, fixture.account.ID,
					"node-"+strconv.Itoa(index), "cn-extra-"+strconv.Itoa(index))
			}
			if got := ConfiguredSlotCapacity(8); got != tc.display {
				t.Fatalf("display capacity = %d, want %d", got, tc.display)
			}
			if got := st.accountCapacityLocked(account, now); got != tc.display {
				t.Fatalf("admission capacity = %d, want %d", got, tc.display)
			}

			users := make([]model.PanelUser, 0, tc.display)
			users = append(users, fixture.user)
			for index := 1; index < tc.display; index++ {
				users = append(users, addLeaseUser(t, st, "bot-"+strconv.Itoa(index), nil))
			}
			for i, user := range users {
				if _, _, err := st.CreateLease(model.CreateLeaseInput{
					UserID: user.ID, Platform: model.PlatformAndroid,
				}); err != nil {
					t.Fatalf("CreateLease #%d: %v", i+1, err)
				}
			}
			extra := addLeaseUser(t, st, "over-capacity", nil)
			_, _, err = st.CreateLease(model.CreateLeaseInput{
				UserID: extra.ID, Platform: model.PlatformAndroid,
			})
			var leaseErr *LeaseError
			if !errors.As(err, &leaseErr) || leaseErr.Reason != "account_slots_exhausted" {
				t.Fatalf("over-capacity create = %v, want account_slots_exhausted", err)
			}
			if got := len(st.Leases()); got != tc.display {
				t.Fatalf("leases = %d, want %d", got, tc.display)
			}
		})
	}
}
