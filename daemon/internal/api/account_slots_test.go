package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestAccountJSONExposesUseCountAndEffectiveSlots proves GET /v1/accounts
// carries both the persisted vendor allowance and the derived lease cap.
func TestAccountJSONExposesUseCountAndEffectiveSlots(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "2")
	ts, st := newTestServer(t)
	if _, err := st.SetAccountUseCount("acc-01", 5); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/accounts/acc-01", nil)
	requireStatus(t, resp, http.StatusOK)
	var one struct {
		UseCount       int `json:"useCount"`
		EffectiveSlots int `json:"effectiveSlots"`
	}
	if err := json.Unmarshal(body, &one); err != nil {
		t.Fatalf("decode account: %v", err)
	}
	if one.UseCount != 5 || one.EffectiveSlots != 2 {
		t.Fatalf("account capacity = useCount %d effectiveSlots %d, want 5/2", one.UseCount, one.EffectiveSlots)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/accounts", nil)
	requireStatus(t, resp, http.StatusOK)
	var rows []struct {
		ID             string `json:"id"`
		UseCount       int    `json:"useCount"`
		EffectiveSlots int    `json:"effectiveSlots"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decode account list: %v", err)
	}
	if len(rows) == 0 || rows[0].ID != "acc-01" || rows[0].UseCount != 5 || rows[0].EffectiveSlots != 2 {
		t.Fatalf("first account capacity = %+v, want acc-01 useCount 5 effectiveSlots 2", rows[0])
	}
}

// TestAccountJSONEffectiveSlotsDefaultsToOne proves the safe default when
// AJS_ACCOUNT_SLOTS is unset: the vendor may report five purchased slots, but
// this daemon still advertises one lease until multi-session is enabled.
func TestAccountJSONEffectiveSlotsDefaultsToOne(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "")
	ts, st := newTestServer(t)
	if _, err := st.SetAccountUseCount("acc-01", 5); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/accounts/acc-01", nil)
	requireStatus(t, resp, http.StatusOK)
	var got struct {
		UseCount       int `json:"useCount"`
		EffectiveSlots int `json:"effectiveSlots"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode account: %v", err)
	}
	if got.UseCount != 5 || got.EffectiveSlots != 1 {
		t.Fatalf("account capacity = useCount %d effectiveSlots %d, want 5/1", got.UseCount, got.EffectiveSlots)
	}
}

// TestCreateLeaseRefusesSecondLeaseWhenSlotsExhausted is the HTTP contract for
// the default one-slot account: a second user on the same account gets a 429
// with the dedicated reason.
func TestCreateLeaseRefusesSecondLeaseWhenSlotsExhausted(t *testing.T) {
	t.Setenv("AJS_ACCOUNT_SLOTS", "")
	fixture := newLeaseHTTPServer(t, Config{})
	if _, err := fixture.st.SetAccountUseCount(fixture.account.ID, 1); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}

	resp, body := createLeaseCall(t, fixture.ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusCreated)

	second := fixture.addUser(t, "second-bot", nil)
	resp, body = createLeaseCall(t, fixture.ts, map[string]any{
		"userId": second.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusTooManyRequests)
	requireErrorCode(t, body, CodeQuotaExceeded)
	requireLeaseReason(t, body, "account_slots_exhausted")
}

// TestCreateLeaseAdmitsUpToCapacity proves admission now counts the slots
// that can actually serve: AJS_ACCOUNT_SLOTS=8 and UseCount=8 are capped by the
// two live nodes of the fixture, so the second lease is admitted and only the
// third is refused with account_slots_exhausted.
func TestCreateLeaseAdmitsUpToCapacity(t *testing.T) {
	fixture := newLeaseHTTPServer(t, Config{})
	t.Setenv("AJS_ACCOUNT_SLOTS", "8")
	if _, err := fixture.st.SetAccountUseCount(fixture.account.ID, 8); err != nil {
		t.Fatalf("SetAccountUseCount: %v", err)
	}

	resp, body := apiCall(t, fixture.ts, http.MethodGet, "/v1/accounts/"+fixture.account.ID, nil)
	requireStatus(t, resp, http.StatusOK)
	var accountRow struct {
		UseCount       int `json:"useCount"`
		EffectiveSlots int `json:"effectiveSlots"`
	}
	if err := json.Unmarshal(body, &accountRow); err != nil {
		t.Fatalf("decode account: %v", err)
	}
	if accountRow.UseCount != 8 || accountRow.EffectiveSlots != 8 {
		t.Fatalf("display capacity = useCount %d effectiveSlots %d, want 8/8",
			accountRow.UseCount, accountRow.EffectiveSlots)
	}

	resp, body = createLeaseCall(t, fixture.ts, map[string]any{
		"userId": fixture.user.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusCreated)
	first := decodeBody[leaseCreateResult](t, body)
	// The warning code is still emitted until the panel step replaces the
	// display/admission split; admission itself now counts serving slots.
	if first.Warning != "multi_session_unimplemented" {
		t.Fatalf("first warning = %q, want multi_session_unimplemented", first.Warning)
	}
	if warning := resp.Header.Get("Warning"); warning == "" {
		t.Fatal("first create did not set the Warning header")
	}

	secondUser := fixture.addUser(t, "second-bot", nil)
	resp, body = createLeaseCall(t, fixture.ts, map[string]any{
		"userId": secondUser.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusCreated)
	second := decodeBody[leaseCreateResult](t, body)
	if second.Lease.NodeID == first.Lease.NodeID {
		t.Fatalf("both leases point at node %s, want one node each", first.Lease.NodeID)
	}

	thirdUser := fixture.addUser(t, "third-bot", nil)
	resp, body = createLeaseCall(t, fixture.ts, map[string]any{
		"userId": thirdUser.ID, "platform": "android",
	}, "")
	requireStatus(t, resp, http.StatusTooManyRequests)
	requireLeaseReason(t, body, "account_slots_exhausted")
	if got := len(fixture.st.Leases()); got != 2 {
		t.Fatalf("leases after refusal = %d, want 2", got)
	}
}
