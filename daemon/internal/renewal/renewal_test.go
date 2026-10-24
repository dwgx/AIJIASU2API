package renewal

import (
	"strings"
	"testing"
	"time"
)

func TestExpiryBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		expires  string
		daysLeft int
		urgency  Urgency
	}{
		{"exactly six days", "2026-10-30T12:00:00Z", 6, UrgencyNone},
		{"five days plus a second", "2026-10-29T12:00:01Z", 6, UrgencyNone},
		{"exactly five days", "2026-10-29T12:00:00Z", 5, UrgencySoon},
		{"one second left", "2026-10-24T12:00:01Z", 1, UrgencySoon},
		{"exactly now", "2026-10-24T12:00:00Z", 0, UrgencyDue},
		{"one second overdue", "2026-10-24T11:59:59Z", -1, UrgencyOverdue},
		{"twelve hours overdue", "2026-10-24T00:00:00Z", -1, UrgencyOverdue},
		{"one day overdue", "2026-10-23T12:00:00Z", -1, UrgencyOverdue},
		{"twenty-five hours overdue", "2026-10-23T11:00:00Z", -2, UrgencyOverdue},
		{"offset input", "2026-10-29T20:00:00+08:00", 5, UrgencySoon},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			renewal, err := Expiry("acct-1", tc.expires, now)
			if err != nil {
				t.Fatalf("Expiry: %v", err)
			}
			if renewal.AccountID != "acct-1" {
				t.Fatalf("AccountID = %q, want acct-1", renewal.AccountID)
			}
			if renewal.ExpiresAt != tc.expires {
				t.Fatalf("ExpiresAt = %q, want %q", renewal.ExpiresAt, tc.expires)
			}
			if renewal.DaysLeft != tc.daysLeft {
				t.Fatalf("DaysLeft = %d, want %d", renewal.DaysLeft, tc.daysLeft)
			}
			if renewal.Urgency != tc.urgency {
				t.Fatalf("Urgency = %q, want %q", renewal.Urgency, tc.urgency)
			}
			if renewal.RenewalURL != OfficialPurchaseURL {
				t.Fatalf("RenewalURL = %q, want %q", renewal.RenewalURL, OfficialPurchaseURL)
			}
		})
	}
}

func TestExpiryRejectsInvalidTimestamp(t *testing.T) {
	if _, err := Expiry("acct-1", "not-a-time", time.Now()); err == nil {
		t.Fatal("Expiry accepted an invalid RFC 3339 timestamp")
	}
}

func TestRenewalURLCarriesNoCredential(t *testing.T) {
	link := RenewalURL()
	if link != OfficialPurchaseURL {
		t.Fatalf("RenewalURL = %q, want %q", link, OfficialPurchaseURL)
	}
	for _, forbidden := range []string{"?", "_client_login", "token", "username", "account"} {
		if strings.Contains(strings.ToLower(link), strings.ToLower(forbidden)) {
			t.Fatalf("renewal URL contains %q: %s", forbidden, link)
		}
	}
}

func TestStatusUsesServiceClock(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	service := New(Config{Now: func() time.Time { return now }})
	status, err := service.Status("acct-7", "2026-10-27T12:00:00Z")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.DaysLeft != 3 || status.Urgency != UrgencySoon {
		t.Fatalf("Status = %+v, want 3 days / soon", status)
	}
}
