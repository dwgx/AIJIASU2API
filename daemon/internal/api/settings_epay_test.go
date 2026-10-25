package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"ajsaccountd/internal/payment"
)

// TestSettingsReportsEpayStatus pins the read-only payment fields the
// dashboard's provider chip reads: epayEnabled is true only when the 易支付
// adapter is both configured and serving checkout, and epayMode names the
// settlement shape. A nil or incomplete adapter reports false and omits the
// mode instead of letting the chip claim a state the routes do not serve.
func TestSettingsReportsEpayStatus(t *testing.T) {
	cases := []struct {
		name    string
		epay    *payment.Epay
		enabled bool
	}{
		{
			name: "serving",
			epay: payment.NewEpay(payment.EpayConfig{
				Gateway: "https://pay.example.test/",
				PID:     epayAPITestPID,
				Key:     epayAPITestKey,
			}),
			enabled: true,
		},
		{
			name:    "unconfigured",
			epay:    payment.NewEpay(payment.EpayConfig{}),
			enabled: false,
		},
		{
			name:    "incomplete",
			epay:    payment.NewEpay(payment.EpayConfig{Gateway: "https://pay.example.test/"}),
			enabled: false,
		},
		{
			name:    "nil adapter",
			epay:    nil,
			enabled: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, _, _ := newOrderTestServer(t, Config{Epay: tc.epay})
			resp, body := apiCall(t, ts, http.MethodGet, "/v1/settings", nil)
			requireStatus(t, resp, http.StatusOK)
			var settings map[string]any
			if err := json.Unmarshal(body, &settings); err != nil {
				t.Fatalf("decode settings: %v", err)
			}
			if got, ok := settings["epayEnabled"]; !ok || got != tc.enabled {
				t.Fatalf("epayEnabled = %v (present %v), want %v", got, ok, tc.enabled)
			}
			mode, hasMode := settings["epayMode"]
			if tc.enabled {
				if !hasMode || mode != tc.epay.Mode() {
					t.Fatalf("epayMode = %v (present %v), want %q", mode, hasMode, tc.epay.Mode())
				}
			} else if hasMode {
				t.Fatalf("epayMode = %v, want it omitted while epay is disabled", mode)
			}
		})
	}
}

// TestSettingsPatchAcceptsReadOnlyEpayFields proves the dashboard can save the
// settings object it read: the read-only payment fields are ignored instead of
// failing the whole PATCH as unknown, and the response still reports the live
// adapter state.
func TestSettingsPatchAcceptsReadOnlyEpayFields(t *testing.T) {
	ts, _, _ := newEpayAPITestServer(t, Config{})
	_, body := apiCall(t, ts, http.MethodGet, "/v1/settings", nil)
	var current map[string]any
	if err := json.Unmarshal(body, &current); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	current["theme"] = "light"

	resp, body := apiCall(t, ts, http.MethodPatch, "/v1/settings", current)
	requireStatus(t, resp, http.StatusOK)
	var updated map[string]any
	if err := json.Unmarshal(body, &updated); err != nil {
		t.Fatalf("decode patched settings: %v", err)
	}
	if updated["theme"] != "light" {
		t.Fatalf("theme = %v, want light", updated["theme"])
	}
	if updated["epayEnabled"] != true || updated["epayMode"] != "webhook" {
		t.Fatalf("read-only payment fields = %v/%v, want true/webhook",
			updated["epayEnabled"], updated["epayMode"])
	}
}
