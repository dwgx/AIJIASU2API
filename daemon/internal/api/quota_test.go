package api

import (
	"net/http"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

// TestAccountTrafficBudgetPatchValidation covers the PATCH contract: a
// non-negative integer sets the budget, an absent field leaves it alone, null
// clears it, and a negative or non-integer value is a 400.
func TestAccountTrafficBudgetPatchValidation(t *testing.T) {
	ts, st := newPlatformServer(t, Config{})

	resp, body := apiCall(t, ts, http.MethodPatch, "/v1/accounts/acc-01",
		map[string]any{"trafficBudgetBytes": 12_345})
	requireStatus(t, resp, http.StatusOK)
	updated := decodeBody[model.Account](t, body)
	if updated.TrafficBudgetBytes == nil || *updated.TrafficBudgetBytes != 12_345 {
		t.Fatalf("patched budget = %v, want 12345 (body %s)", updated.TrafficBudgetBytes, body)
	}
	stored, _ := st.Account("acc-01")
	if stored.TrafficBudgetBytes == nil || *stored.TrafficBudgetBytes != 12_345 {
		t.Fatalf("stored budget = %v, want 12345", stored.TrafficBudgetBytes)
	}

	// An absent field leaves the budget untouched.
	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/accounts/acc-01",
		map[string]any{"concurrency": 120})
	requireStatus(t, resp, http.StatusOK)
	unchanged := decodeBody[model.Account](t, body)
	if unchanged.TrafficBudgetBytes == nil || *unchanged.TrafficBudgetBytes != 12_345 {
		t.Fatalf("budget after unrelated patch = %v, want 12345", unchanged.TrafficBudgetBytes)
	}

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/accounts/acc-01",
		map[string]any{"trafficBudgetBytes": -1})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/accounts/acc-01",
		map[string]any{"trafficBudgetBytes": "lots"})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	// Explicit null clears the budget (unlimited).
	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/accounts/acc-01",
		map[string]any{"trafficBudgetBytes": nil})
	requireStatus(t, resp, http.StatusOK)
	cleared := decodeBody[model.Account](t, body)
	if cleared.TrafficBudgetBytes != nil {
		t.Fatalf("cleared budget = %v, want null", *cleared.TrafficBudgetBytes)
	}

	resp, body = apiCall(t, ts, http.MethodPatch, "/v1/accounts/acc-missing",
		map[string]any{"trafficBudgetBytes": 1})
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)
}

// quotaUsageSummary mirrors the enforcement fields this suite asserts.
type quotaUsageSummary struct {
	QuotaEnforced bool `json:"quotaEnforced"`
	Accounts      []struct {
		Account            string `json:"account"`
		TrafficBudgetBytes *int64 `json:"trafficBudgetBytes"`
		UsedBytes          int64  `json:"usedBytes"`
		RemainingBytes     *int64 `json:"remainingBytes"`
	} `json:"accounts"`
}

// TestUsageReportsEnforcedAccountBudget proves GET /v1/usage reports
// quotaEnforced=true with the per-account budget, the current UTC month's
// gate-metered bytes and the remaining headroom.
func TestUsageReportsEnforcedAccountBudget(t *testing.T) {
	ts, st := newPlatformServer(t, Config{Usage: usageAggregator()})
	now := time.Now().UTC()

	// Seeded rows are proxy-sourced and must not consume the budget; this
	// gate row is the month's only billable traffic.
	st.AppendRequest(model.RequestLog{
		TS:           now.Format(time.RFC3339),
		AccountLabel: "Northwind Ops",
		Target:       "example.com:443",
		Status:       http.StatusOK,
		Bytes:        400,
		Source:       model.SourceGate,
	})
	budget := int64(1_000)
	resp, body := apiCall(t, ts, http.MethodPatch, "/v1/accounts/acc-01",
		map[string]any{"trafficBudgetBytes": budget})
	requireStatus(t, resp, http.StatusOK)

	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Add(time.Hour).Format(time.RFC3339)
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/usage?from="+from+"&to="+to, nil)
	requireStatus(t, resp, http.StatusOK)
	summary := decodeBody[quotaUsageSummary](t, body)
	if !summary.QuotaEnforced {
		t.Fatalf("quotaEnforced = false with a budget set (body %s)", body)
	}
	for _, row := range summary.Accounts {
		if row.Account != "Northwind Ops" {
			continue
		}
		if row.TrafficBudgetBytes == nil || *row.TrafficBudgetBytes != budget {
			t.Fatalf("account budget = %v, want %d", row.TrafficBudgetBytes, budget)
		}
		if row.UsedBytes != 400 {
			t.Fatalf("account usedBytes = %d, want 400 (gate rows only)", row.UsedBytes)
		}
		if row.RemainingBytes == nil || *row.RemainingBytes != 600 {
			t.Fatalf("account remainingBytes = %v, want 600", row.RemainingBytes)
		}
		return
	}
	t.Fatalf("usage summary has no Northwind Ops row: %s", body)
}
