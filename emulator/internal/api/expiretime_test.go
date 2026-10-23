package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fixedLocal pins time.Local to a fixed offset for one test and restores it
// afterwards, so zoneless ExpireTime layouts decode deterministically on any
// host. Tests that use it must not run in parallel.
func fixedLocal(t *testing.T, offsetSeconds int) *time.Location {
	t.Helper()
	fixed := time.FixedZone("test-fixed", offsetSeconds)
	original := time.Local
	time.Local = fixed
	t.Cleanup(func() { time.Local = original })
	return fixed
}

// loginEnvelope wraps one ExpireTime JSON fragment in the Login object shape
// the control API returns, with sentinel credentials the error path must never
// echo.
func loginEnvelope(expireTime string) []byte {
	return []byte(`{"AuthResult":"OK","LoginToken":"tok-1|user","UserName":"user",` +
		`"ConnectPassword":"cp-1","MembershipStatus":"vip","GroupTitle":"VIP",` +
		`"Expired":false,"UseCount":3,` + expireTime + `}`)
}

// TestLoginResultExpireTimeShapes pins every accepted ExpireTime shape: the
// public field stays int64 Unix milliseconds whether the vendor sends the
// legacy number, a numeric string, or a date/time string.
func TestLoginResultExpireTimeShapes(t *testing.T) {
	local := fixedLocal(t, 8*60*60) // UTC+8, not the usual host zone
	dateLocal := time.Date(2026, 12, 21, 17, 9, 35, 0, local).UnixMilli()
	midnightLocal := time.Date(2026, 12, 21, 0, 0, 0, 0, local).UnixMilli()
	rfc3339 := time.Date(2026, 12, 21, 9, 9, 35, 0, time.UTC).UnixMilli()
	rfc3339Nano := time.Date(2026, 12, 21, 9, 9, 35, 500000000, time.UTC).UnixMilli()

	cases := []struct {
		name       string
		expireTime string
		want       int64
	}{
		{"number unchanged", `"ExpireTime":1767225600000`, 1767225600000},
		{"numeric string", `"ExpireTime":"1767225600000"`, 1767225600000},
		{"local datetime", `"ExpireTime":"2026-12-21 17:09:35"`, dateLocal},
		{"local T datetime", `"ExpireTime":"2026-12-21T17:09:35"`, dateLocal},
		{"local date only", `"ExpireTime":"2026-12-21"`, midnightLocal},
		{"rfc3339 with offset", `"ExpireTime":"2026-12-21T17:09:35+08:00"`, rfc3339},
		{"rfc3339 nano", `"ExpireTime":"2026-12-21T17:09:35.5+08:00"`, rfc3339Nano},
		{"absent field", `"UseCount":3`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var login LoginResult
			if err := json.Unmarshal(loginEnvelope(tc.expireTime), &login); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if login.ExpireTime != tc.want {
				t.Fatalf("ExpireTime = %d, want %d", login.ExpireTime, tc.want)
			}
			// The other fields must still decode: the custom unmarshaler only
			// replaces ExpireTime handling.
			if login.AuthResult != "OK" || login.LoginToken != "tok-1|user" ||
				login.UserName != "user" || login.ConnectPassword != "cp-1" ||
				login.MembershipStatus != "vip" || login.GroupTitle != "VIP" ||
				login.Expired || login.UseCount != 3 {
				t.Fatalf("surrounding fields = %+v", login)
			}
		})
	}
}

// TestLoginResultExpireTimeErrors pins the rejection path: an unsupported
// value fails the decode, the error names the ExpireTime value and the
// accepted shapes, and it never echoes the token or connect password.
func TestLoginResultExpireTimeErrors(t *testing.T) {
	fixedLocal(t, 0)

	cases := []struct {
		name       string
		expireTime string
		wantSub    string
	}{
		{"unknown string", `"ExpireTime":"not-a-time"`, "not-a-time"},
		{"fractional number", `"ExpireTime":1767225600.5`, "1767225600.5"},
		{"json array", `"ExpireTime":[1767225600000]`, "[1767225600000]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var login LoginResult
			err := json.Unmarshal(loginEnvelope(tc.expireTime), &login)
			if err == nil {
				t.Fatalf("Unmarshal succeeded with ExpireTime %d, want error", login.ExpireTime)
			}
			msg := err.Error()
			if !strings.Contains(msg, "ExpireTime") {
				t.Fatalf("error %q does not mention ExpireTime", msg)
			}
			if !strings.Contains(msg, tc.wantSub) {
				t.Fatalf("error %q does not include the value %q", msg, tc.wantSub)
			}
			for _, secret := range []string{"tok-1", "cp-1", "user"} {
				if strings.Contains(msg, secret) {
					t.Fatalf("error %q leaks %q", msg, secret)
				}
			}
			if !strings.Contains(msg, "2006-01-02") {
				t.Fatalf("error %q does not list the accepted shapes", msg)
			}
		})
	}
}

// TestLoginDecodesStringExpireTime replays the live failure end to end: the
// vendor sent ExpireTime as a date string and json.Unmarshal rejected it with
// "cannot unmarshal string into Go struct field ... of type int64". Login must
// now decode that response and report Unix milliseconds.
func TestLoginDecodesStringExpireTime(t *testing.T) {
	local := fixedLocal(t, 8*60*60)
	want := time.Date(2026, 12, 21, 17, 9, 35, 0, local).UnixMilli()

	device := DefaultDevice()
	if err := device.EnsureIDs(); err != nil {
		t.Fatalf("EnsureIDs: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Login":{"AuthResult":"OK","LoginToken":"tok|user",`+
			`"UserName":"u1","ConnectPassword":"cp-1",`+
			`"ExpireTime":"2026-12-21 17:09:35","Expired":false}}`)
	}))
	defer srv.Close()

	client := NewClient([]string{srv.URL}, device)
	client.HTTP = srv.Client()

	result, err := client.Login(context.Background(), "user@example.com", "p@ss")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.ExpireTime != want {
		t.Fatalf("ExpireTime = %d, want %d (%s in %s)",
			result.ExpireTime, want, "2026-12-21 17:09:35", local)
	}
	if result.LoginToken != "tok|user" || result.ConnectPassword != "cp-1" {
		t.Fatalf("session fields = %q/%q", result.LoginToken, result.ConnectPassword)
	}
}
