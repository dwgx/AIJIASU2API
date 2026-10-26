package webhook

import (
	"reflect"
	"testing"
)

// TestLeaseEventTypesAreInTheCatalogue pins the wire strings. They are the
// X-APC-Event value and the payload's event field, so a rename is a
// customer-facing breaking change.
func TestLeaseEventTypesAreInTheCatalogue(t *testing.T) {
	cases := []struct {
		event EventType
		want  string
	}{
		{EventLeaseCreated, "lease.created"},
		{EventLeaseReissued, "lease.reissued"},
		{EventLeaseRevoked, "lease.revoked"},
		{EventLeaseExpired, "lease.expired"},
	}
	for _, tc := range cases {
		if string(tc.event) != tc.want {
			t.Errorf("event = %q, want %q", tc.event, tc.want)
		}
		if !tc.event.Valid() {
			t.Errorf("%q is not a valid event", tc.event)
		}
	}
	if EventType("lease.rotated").Valid() {
		t.Error("an undefined lease event was accepted")
	}

	// A receiver may subscribe to the lease events; validation would
	// reject an unknown type before the endpoint could ever deliver.
	endpoint := Endpoint{
		ID:     "ep",
		URL:    "https://93.184.216.34/hook",
		Secret: "s",
		Events: []EventType{EventLeaseCreated, EventLeaseReissued, EventLeaseRevoked, EventLeaseExpired},
	}
	if err := validateEndpoint(endpoint, false, false); err != nil {
		t.Fatalf("endpoint with lease subscriptions rejected: %v", err)
	}
}

// TestLeaseEventDataCarriesOnlyTheLeaseContract proves the payload has the
// documented five fields, no credential-shaped key, and survives Scrub
// unchanged: nothing in a lease event can be redacted on the wire.
func TestLeaseEventDataCarriesOnlyTheLeaseContract(t *testing.T) {
	data := LeaseEventData("lease-07", "user-02", "android", "acc-03", "2026-10-25T20:00:00Z")
	want := map[string]any{
		"leaseId":   "lease-07",
		"userId":    "user-02",
		"platform":  "android",
		"accountId": "acc-03",
		"expiresAt": "2026-10-25T20:00:00Z",
	}
	if !reflect.DeepEqual(data, want) {
		t.Fatalf("LeaseEventData = %#v, want %#v", data, want)
	}
	if scrubbed := Scrub(data); !reflect.DeepEqual(scrubbed, want) {
		t.Fatalf("Scrub(lease payload) = %#v, want the payload unchanged", scrubbed)
	}
	for _, forbidden := range []string{"key", "secret", "token", "password", "idempotencyKey"} {
		if _, ok := data[forbidden]; ok {
			t.Errorf("lease payload carries the credential field %q", forbidden)
		}
	}
}
