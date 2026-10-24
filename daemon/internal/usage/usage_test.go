package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

var testNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

func logRow(ts, userID, userName, account, target string, status int, n int64, latency int) model.RequestLog {
	return model.RequestLog{
		ID:           fmt.Sprintf("%s|%s|%s", ts, userID, target),
		TS:           ts,
		UserID:       userID,
		UserName:     userName,
		AccountLabel: account,
		Target:       target,
		Status:       status,
		Bytes:        n,
		LatencyMs:    latency,
	}
}

func TestAggregateMultiDayMultiAccount(t *testing.T) {
	agg := New(Config{Now: func() time.Time { return testNow }})
	logs := []model.RequestLog{
		logRow("2026-10-01T01:00:00Z", "u1", "ada", "acct-a", "api.example.com:443", 200, 100, 10),
		logRow("2026-10-01T02:00:00Z", "u1", "ada", "acct-a", "api.example.com:443", 502, 50, 30),
		logRow("2026-10-02T03:00:00Z", "u2", "bob", "acct-b", "cdn.example.net:443", 204, 200, 20),
		logRow("2026-10-02T04:00:00Z", "u1", "ada", "acct-a", "https://auth.example.org/v1/login?token=secret", 200, 100, 41),
		// Outside the window, unparseable, and outside the window again.
		logRow("2026-09-30T23:59:59Z", "u1", "ada", "acct-a", "api.example.com:443", 200, 999, 1),
		logRow("not-a-time", "u2", "bob", "acct-b", "cdn.example.net:443", 200, 999, 1),
		logRow("2026-10-04T00:00:00Z", "u2", "bob", "acct-b", "cdn.example.net:443", 200, 999, 1),
	}

	got, err := agg.Aggregate(logs, Query{
		From: mustTime(t, "2026-10-01T00:00:00Z"),
		To:   mustTime(t, "2026-10-03T23:59:59Z"),
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}

	if got.From != "2026-10-01T00:00:00Z" || got.To != "2026-10-03T23:59:59Z" {
		t.Fatalf("window = %s..%s", got.From, got.To)
	}
	if got.Requests != 4 || got.Bytes != 450 || got.Errors != 1 {
		t.Fatalf("totals = requests %d bytes %d errors %d, want 4/450/1", got.Requests, got.Bytes, got.Errors)
	}
	if got.Truncated {
		t.Fatal("Truncated = true for a complete scan")
	}

	wantDays := []Bucket{
		{Day: "2026-10-01", Requests: 2, Bytes: 150, Errors: 1, AvgLatencyMs: 20},
		{Day: "2026-10-02", Requests: 2, Bytes: 300, Errors: 0, AvgLatencyMs: 31},
		{Day: "2026-10-03"},
	}
	if !reflect.DeepEqual(got.Days, wantDays) {
		t.Fatalf("Days = %+v, want %+v", got.Days, wantDays)
	}

	wantAccounts := []AccountUsage{
		{
			Account:      "acct-a",
			Requests:     3,
			Bytes:        250,
			Errors:       1,
			AvgLatencyMs: 27,
			Buckets: []Bucket{
				{Day: "2026-10-01", Requests: 2, Bytes: 150, Errors: 1, AvgLatencyMs: 20},
				{Day: "2026-10-02", Requests: 1, Bytes: 100, AvgLatencyMs: 41},
				{Day: "2026-10-03"},
			},
		},
		{
			Account:      "acct-b",
			Requests:     1,
			Bytes:        200,
			AvgLatencyMs: 20,
			Buckets: []Bucket{
				{Day: "2026-10-01"},
				{Day: "2026-10-02", Requests: 1, Bytes: 200, AvgLatencyMs: 20},
				{Day: "2026-10-03"},
			},
		},
	}
	if !reflect.DeepEqual(got.Accounts, wantAccounts) {
		t.Fatalf("Accounts = %+v, want %+v", got.Accounts, wantAccounts)
	}

	wantUsers := []UserUsage{
		{UserID: "u1", UserName: "ada", Requests: 3, Bytes: 250, UsedBytes: 250},
		{UserID: "u2", UserName: "bob", Requests: 1, Bytes: 200, UsedBytes: 200},
	}
	if !reflect.DeepEqual(got.Users, wantUsers) {
		t.Fatalf("Users = %+v, want %+v", got.Users, wantUsers)
	}

	wantTargets := []TargetUsage{
		{Target: "api.example.com", Requests: 2, Bytes: 150},
		{Target: "cdn.example.net", Requests: 1, Bytes: 200},
		{Target: "auth.example.org", Requests: 1, Bytes: 100},
	}
	if !reflect.DeepEqual(got.TopTargets, wantTargets) {
		t.Fatalf("TopTargets = %+v, want %+v", got.TopTargets, wantTargets)
	}
}

func TestAggregateEmptyDataZeroFillsDays(t *testing.T) {
	agg := New(Config{})
	got, err := agg.Aggregate(nil, Query{
		From: mustTime(t, "2026-10-01T00:00:00Z"),
		To:   mustTime(t, "2026-10-02T23:59:59Z"),
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if got.Requests != 0 || got.Bytes != 0 || got.Errors != 0 || got.Truncated {
		t.Fatalf("empty totals = %+v", got)
	}
	wantDays := []Bucket{{Day: "2026-10-01"}, {Day: "2026-10-02"}}
	if !reflect.DeepEqual(got.Days, wantDays) {
		t.Fatalf("Days = %+v, want %+v", got.Days, wantDays)
	}
	if got.Accounts == nil || len(got.Accounts) != 0 {
		t.Fatalf("Accounts = %#v, want non-nil empty slice", got.Accounts)
	}
	if got.Users == nil || len(got.Users) != 0 {
		t.Fatalf("Users = %#v, want non-nil empty slice", got.Users)
	}
	if got.TopTargets == nil || len(got.TopTargets) != 0 {
		t.Fatalf("TopTargets = %#v, want non-nil empty slice", got.TopTargets)
	}
}

func TestWindowBounds(t *testing.T) {
	agg := New(Config{Now: func() time.Time { return testNow }})

	short := New(Config{MaxWindowDays: 2})
	if _, err := short.Aggregate(nil, Query{
		From: mustTime(t, "2026-10-01T00:00:00Z"),
		To:   mustTime(t, "2026-10-03T00:00:00Z"),
	}); !errors.Is(err, ErrWindowTooLong) {
		t.Fatalf("configured 2-day limit error = %v, want ErrWindowTooLong", err)
	}

	var zeroValue Aggregator
	zeroSummary, err := zeroValue.Aggregate([]model.RequestLog{
		logRow("2026-10-01T01:00:00Z", "u1", "ada", "acct-a", "h.example", 200, 1, 1),
	}, Query{
		From: mustTime(t, "2026-10-01T00:00:00Z"),
		To:   mustTime(t, "2026-10-01T23:59:59Z"),
	})
	if err != nil {
		t.Fatalf("zero-value aggregator: %v", err)
	}
	if zeroSummary.Requests != 1 || zeroSummary.Bytes != 1 {
		t.Fatalf("zero-value aggregator summary = %+v", zeroSummary)
	}

	if _, err := agg.Aggregate(nil, Query{
		From: mustTime(t, "2026-10-02T00:00:00Z"),
		To:   mustTime(t, "2026-10-01T00:00:00Z"),
	}); !errors.Is(err, ErrInvalidWindow) {
		t.Fatalf("inverted window error = %v, want ErrInvalidWindow", err)
	}

	if _, err := agg.Aggregate(nil, Query{
		From: mustTime(t, "2026-01-01T00:00:00Z"),
		To:   mustTime(t, "2027-01-02T00:00:00Z"),
	}); !errors.Is(err, ErrWindowTooLong) {
		t.Fatalf("367-day window error = %v, want ErrWindowTooLong", err)
	}

	got, err := agg.Aggregate(nil, Query{
		From: mustTime(t, "2026-01-01T00:00:00Z"),
		To:   mustTime(t, "2027-01-01T00:00:00Z"),
	})
	if err != nil {
		t.Fatalf("366-day window: %v", err)
	}
	if len(got.Days) != 366 || got.Days[0].Day != "2026-01-01" || got.Days[365].Day != "2027-01-01" {
		t.Fatalf("366-day series = %d buckets (%s..%s)", len(got.Days), got.Days[0].Day, got.Days[len(got.Days)-1].Day)
	}

	instant, err := agg.Aggregate(nil, Query{
		From: mustTime(t, "2026-10-24T12:00:00Z"),
		To:   mustTime(t, "2026-10-24T12:00:00Z"),
	})
	if err != nil {
		t.Fatalf("instant window: %v", err)
	}
	if len(instant.Days) != 1 || instant.Days[0].Day != "2026-10-24" {
		t.Fatalf("instant window Days = %+v", instant.Days)
	}

	canonical, err := agg.Aggregate(nil, Query{
		From: mustTime(t, "2026-10-01T02:00:00+02:00"),
		To:   mustTime(t, "2026-10-02T02:00:00+02:00"),
	})
	if err != nil {
		t.Fatalf("offset window: %v", err)
	}
	if canonical.From != "2026-10-01T00:00:00Z" || canonical.To != "2026-10-02T00:00:00Z" {
		t.Fatalf("canonical window = %s..%s", canonical.From, canonical.To)
	}

	defaulted, err := agg.Aggregate(nil, Query{})
	if err != nil {
		t.Fatalf("default window: %v", err)
	}
	if defaulted.From != "2026-09-24T12:00:00Z" || defaulted.To != "2026-10-24T12:00:00Z" {
		t.Fatalf("default window = %s..%s", defaulted.From, defaulted.To)
	}
	if len(defaulted.Days) != 31 {
		t.Fatalf("default window Days = %d, want 31", len(defaulted.Days))
	}

	fromOnly, err := agg.Aggregate(nil, Query{To: mustTime(t, "2026-10-01T00:00:00Z")})
	if err != nil {
		t.Fatalf("from-only window: %v", err)
	}
	if fromOnly.From != "2026-09-01T00:00:00Z" {
		t.Fatalf("from-only From = %s, want 2026-09-01T00:00:00Z", fromOnly.From)
	}
}

func TestAggregateFilters(t *testing.T) {
	agg := New(Config{})
	logs := []model.RequestLog{
		logRow("2026-10-01T01:00:00Z", "u1", "ada", "acct-a", "a.example.com", 200, 10, 1),
		logRow("2026-10-01T02:00:00Z", "u2", "bob", "acct-a", "b.example.com", 200, 20, 1),
		logRow("2026-10-01T03:00:00Z", "u1", "ada", "acct-b", "c.example.com", 200, 30, 1),
	}
	window := Query{
		From: mustTime(t, "2026-10-01T00:00:00Z"),
		To:   mustTime(t, "2026-10-01T23:59:59Z"),
	}

	byAccount := window
	byAccount.Account = "acct-a"
	got, err := agg.Aggregate(logs, byAccount)
	if err != nil {
		t.Fatalf("account filter: %v", err)
	}
	if got.Requests != 2 || got.Bytes != 30 || len(got.Accounts) != 1 || len(got.Users) != 2 {
		t.Fatalf("account filter = %+v", got)
	}

	byUser := window
	byUser.UserID = "u1"
	got, err = agg.Aggregate(logs, byUser)
	if err != nil {
		t.Fatalf("user filter: %v", err)
	}
	if got.Requests != 2 || got.Bytes != 40 || len(got.Accounts) != 2 || len(got.Users) != 1 {
		t.Fatalf("user filter = %+v", got)
	}

	both := window
	both.Account = "acct-b"
	both.UserID = "u1"
	got, err = agg.Aggregate(logs, both)
	if err != nil {
		t.Fatalf("combined filter: %v", err)
	}
	if got.Requests != 1 || got.Bytes != 30 || len(got.Accounts) != 1 || len(got.Users) != 1 {
		t.Fatalf("combined filter = %+v", got)
	}

	none := window
	none.Account = "acct-z"
	got, err = agg.Aggregate(logs, none)
	if err != nil {
		t.Fatalf("miss filter: %v", err)
	}
	if got.Requests != 0 || len(got.Accounts) != 0 || len(got.Users) != 0 || len(got.TopTargets) != 0 {
		t.Fatalf("miss filter = %+v", got)
	}
}

func TestQuotaPassThrough(t *testing.T) {
	agg := New(Config{})
	quota := int64(1_000)
	quotas := map[string]*int64{
		"u1": &quota,
		"u3": nil,
	}
	logs := []model.RequestLog{
		logRow("2026-10-01T01:00:00Z", "u1", "ada", "acct-a", "a.example.com", 200, 100, 1),
		logRow("2026-10-01T02:00:00Z", "u2", "bob", "acct-a", "a.example.com", 200, 200, 1),
		logRow("2026-10-01T03:00:00Z", "u3", "cy", "acct-a", "a.example.com", 200, 50, 1),
	}
	got, err := agg.Aggregate(logs, Query{
		From:   mustTime(t, "2026-10-01T00:00:00Z"),
		To:     mustTime(t, "2026-10-01T23:59:59Z"),
		Quotas: quotas,
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if len(got.Users) != 3 {
		t.Fatalf("Users = %+v", got.Users)
	}
	byID := make(map[string]UserUsage, len(got.Users))
	for _, user := range got.Users {
		byID[user.UserID] = user
	}
	if user := byID["u1"]; user.QuotaBytesMonth != quotas["u1"] || user.UsedBytes != 100 || user.Bytes != 100 {
		t.Fatalf("u1 = %+v, want the exact quota pointer and 100 used bytes", user)
	}
	if user := byID["u2"]; user.QuotaBytesMonth != nil || user.UsedBytes != 200 {
		t.Fatalf("u2 = %+v, want nil quota", user)
	}
	if user := byID["u3"]; user.QuotaBytesMonth != nil || user.UsedBytes != 50 {
		t.Fatalf("u3 = %+v, want explicit nil quota", user)
	}
	if byID["u1"].QuotaBytesMonth == nil || *byID["u1"].QuotaBytesMonth != 1_000 {
		t.Fatalf("quota value was not passed through: %+v", byID["u1"].QuotaBytesMonth)
	}
}

func TestSortTieBreak(t *testing.T) {
	agg := New(Config{})
	logs := []model.RequestLog{
		logRow("2026-10-01T01:00:00Z", "u-zed", "zed", "acct-b", "a.example.com", 200, 100, 1),
		logRow("2026-10-01T02:00:00Z", "u-amy", "amy", "acct-a", "a.example.com", 200, 100, 1),
		logRow("2026-10-01T03:00:00Z", "u-bob", "bob", "acct-c", "a.example.com", 200, 200, 1),
	}
	got, err := agg.Aggregate(logs, Query{
		From: mustTime(t, "2026-10-01T00:00:00Z"),
		To:   mustTime(t, "2026-10-01T23:59:59Z"),
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	var accounts []string
	for _, account := range got.Accounts {
		accounts = append(accounts, account.Account)
	}
	if want := []string{"acct-c", "acct-a", "acct-b"}; !reflect.DeepEqual(accounts, want) {
		t.Fatalf("account order = %v, want %v", accounts, want)
	}
	var users []string
	for _, user := range got.Users {
		users = append(users, user.UserName)
	}
	if want := []string{"bob", "amy", "zed"}; !reflect.DeepEqual(users, want) {
		t.Fatalf("user order = %v, want %v", users, want)
	}
}

func TestHostLabel(t *testing.T) {
	cases := []struct {
		target string
		want   string
	}{
		{"https://api.example.com:8443/v1/items?token=secret", "api.example.com"},
		{"api.example.com:443", "api.example.com"},
		{"user:pass@api.example.com:443/path?q=1", "api.example.com"},
		{"https://[2001:db8::1]:443/x?q=1", "2001:db8::1"},
		{"//cdn.example.net:443/x", "cdn.example.net"},
		{"API.Example.COM.", "api.example.com"},
		{"api.example.com/path?q=1", "api.example.com"},
		{"api.example.com?q=1", "api.example.com"},
		{"user@host:443", "host"},
		{"host:notaport", "host"},
		{"localhost:8080", "localhost"},
		{"[2001:db8::1]", "2001:db8::1"},
		{"2001:db8::1", "2001:db8::1"},
		{"", UnknownTarget},
		{"   ", UnknownTarget},
		{"http://", UnknownTarget},
	}
	for _, tc := range cases {
		if got := hostLabel(tc.target); got != tc.want {
			t.Errorf("hostLabel(%q) = %q, want %q", tc.target, got, tc.want)
		}
	}
}

func TestTopTargetsRedaction(t *testing.T) {
	agg := New(Config{})
	logs := []model.RequestLog{
		logRow("2026-10-01T01:00:00Z", "u1", "ada", "acct-a", "https://api.example.com:8443/v1/items?token=secret#frag", 200, 10, 1),
		logRow("2026-10-01T02:00:00Z", "u1", "ada", "acct-a", "api.example.com:443", 200, 20, 1),
		logRow("2026-10-01T03:00:00Z", "u1", "ada", "acct-a", "user:pass@api.example.com:443/path?q=1", 200, 30, 1),
		logRow("2026-10-01T04:00:00Z", "u1", "ada", "acct-a", "https://[2001:db8::1]:443/x?q=1", 200, 40, 1),
		logRow("2026-10-01T05:00:00Z", "u1", "ada", "acct-a", "", 200, 50, 1),
		logRow("2026-10-01T06:00:00Z", "u1", "ada", "acct-a", "API.Example.COM.", 200, 60, 1),
	}
	got, err := agg.Aggregate(logs, Query{
		From: mustTime(t, "2026-10-01T00:00:00Z"),
		To:   mustTime(t, "2026-10-01T23:59:59Z"),
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	want := []TargetUsage{
		{Target: "api.example.com", Requests: 4, Bytes: 120},
		{Target: UnknownTarget, Requests: 1, Bytes: 50},
		{Target: "2001:db8::1", Requests: 1, Bytes: 40},
	}
	if !reflect.DeepEqual(got.TopTargets, want) {
		t.Fatalf("TopTargets = %+v, want %+v", got.TopTargets, want)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leaked := range []string{"secret", "token", "8443", "user:pass", "?", "#", "/"} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("summary leaked %q: %s", leaked, encoded)
		}
	}
}

func TestTopTargetsLimit(t *testing.T) {
	agg := New(Config{})
	logs := []model.RequestLog{
		logRow("2026-10-01T00:00:00Z", "u1", "ada", "acct-a", "hot.example", 200, 1, 1),
		logRow("2026-10-01T00:00:01Z", "u1", "ada", "acct-a", "hot.example", 200, 1, 1),
	}
	for i := range 12 {
		logs = append(logs, logRow("2026-10-01T00:00:02Z", "u1", "ada", "acct-a", fmt.Sprintf("h%02d.example", i), 200, 1, 1))
	}
	got, err := agg.Aggregate(logs, Query{
		From: mustTime(t, "2026-10-01T00:00:00Z"),
		To:   mustTime(t, "2026-10-01T23:59:59Z"),
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if len(got.TopTargets) != TopTargetsLimit {
		t.Fatalf("TopTargets = %d entries, want %d", len(got.TopTargets), TopTargetsLimit)
	}
	if got.TopTargets[0].Target != "hot.example" || got.TopTargets[0].Requests != 2 {
		t.Fatalf("TopTargets[0] = %+v", got.TopTargets[0])
	}
	for i := 1; i < len(got.TopTargets); i++ {
		if want := fmt.Sprintf("h%02d.example", i-1); got.TopTargets[i].Target != want {
			t.Fatalf("TopTargets[%d] = %q, want %q", i, got.TopTargets[i].Target, want)
		}
	}
}

func TestRowCap(t *testing.T) {
	agg := New(Config{})
	window := Query{
		From: mustTime(t, "2026-10-24T00:00:00Z"),
		To:   mustTime(t, "2026-10-24T23:59:59Z"),
	}
	row := model.RequestLog{
		TS:           "2026-10-24T01:00:00Z",
		UserID:       "u1",
		UserName:     "ada",
		AccountLabel: "acct-a",
		Target:       "h.example",
		Status:       200,
		Bytes:        1,
		LatencyMs:    1,
	}
	outside := row
	outside.TS = "2026-10-25T01:00:00Z"

	logs := make([]model.RequestLog, 0, MaxRows+2)
	logs = append(logs, outside)
	for range MaxRows + 1 {
		logs = append(logs, row)
	}

	got, err := agg.Aggregate(logs, window)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true past the row cap")
	}
	if got.Requests != MaxRows || got.Bytes != MaxRows {
		t.Fatalf("totals = requests %d bytes %d, want %d/%d", got.Requests, got.Bytes, MaxRows, MaxRows)
	}
	if len(got.Days) != 1 || got.Days[0].Requests != MaxRows {
		t.Fatalf("Days = %+v", got.Days)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].Requests != MaxRows {
		t.Fatalf("Accounts = %+v", got.Accounts)
	}
	if len(got.Users) != 1 || got.Users[0].Requests != MaxRows {
		t.Fatalf("Users = %+v", got.Users)
	}
	if len(got.TopTargets) != 1 || got.TopTargets[0].Requests != MaxRows {
		t.Fatalf("TopTargets = %+v", got.TopTargets)
	}

	// Exactly MaxRows in-window rows, followed by an out-of-window row, must
	// not report truncation: the cap counts in-window rows only.
	exact := append(append([]model.RequestLog{}, logs[:MaxRows+1]...), outside)
	got, err = agg.Aggregate(exact, window)
	if err != nil {
		t.Fatalf("Aggregate exact cap: %v", err)
	}
	if got.Truncated {
		t.Fatal("Truncated = true at exactly the cap")
	}
	if got.Requests != MaxRows {
		t.Fatalf("exact cap requests = %d, want %d", got.Requests, MaxRows)
	}
}

func TestConfigurableRowCapSkipsOutsideRows(t *testing.T) {
	agg := New(Config{MaxRows: 2})
	outside := logRow("2026-10-02T00:00:00Z", "u1", "ada", "acct-a", "h.example", 200, 1, 1)
	logs := []model.RequestLog{
		outside,
		logRow("2026-10-01T01:00:00Z", "u1", "ada", "acct-a", "h.example", 200, 1, 1),
		logRow("2026-10-01T02:00:00Z", "u1", "ada", "acct-a", "h.example", 200, 1, 1),
		outside,
		logRow("2026-10-01T03:00:00Z", "u1", "ada", "acct-a", "h.example", 200, 1, 1),
	}
	window := Query{
		From: mustTime(t, "2026-10-01T00:00:00Z"),
		To:   mustTime(t, "2026-10-01T23:59:59Z"),
	}
	got, err := agg.Aggregate(logs, window)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if !got.Truncated || got.Requests != 2 {
		t.Fatalf("truncated cap = %+v, want truncated with 2 requests", got)
	}

	got, err = agg.Aggregate(logs[:4], window)
	if err != nil {
		t.Fatalf("Aggregate under cap: %v", err)
	}
	if got.Truncated || got.Requests != 2 {
		t.Fatalf("under cap = %+v, want 2 requests and no truncation", got)
	}
}

func TestAggregateConcurrent(t *testing.T) {
	agg := New(Config{Now: func() time.Time { return testNow }})
	logs := make([]model.RequestLog, 0, 400)
	for i := range 400 {
		day := i%3 + 1
		logs = append(logs, logRow(
			fmt.Sprintf("2026-10-0%dT01:00:00Z", day),
			fmt.Sprintf("u%d", i%4),
			fmt.Sprintf("user%d", i%4),
			fmt.Sprintf("acct-%d", i%2),
			fmt.Sprintf("host%d.example", i%7),
			200+i%2*200,
			int64(i*13),
			i%90,
		))
	}
	query := Query{
		From: mustTime(t, "2026-10-01T00:00:00Z"),
		To:   mustTime(t, "2026-10-03T23:59:59Z"),
	}
	want, err := agg.Aggregate(logs, query)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}

	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 25 {
				got, err := agg.Aggregate(logs, query)
				if err != nil {
					errs <- err
					return
				}
				if !reflect.DeepEqual(got, want) {
					errs <- errors.New("concurrent summary mismatch")
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestAggregateIgnoresMalformedTimestamps(t *testing.T) {
	agg := New(Config{})
	got, err := agg.Aggregate([]model.RequestLog{
		{ID: "bad-1", TS: "yesterday", Bytes: 10},
		{ID: "bad-2", TS: "2026-10-01", Bytes: 10},
		{ID: "good", TS: "2026-10-01T12:00:00Z", Bytes: 5, UserID: "u1", AccountLabel: "a"},
	}, Query{
		From: mustTime(t, "2026-10-01T00:00:00Z"),
		To:   mustTime(t, "2026-10-01T23:59:59Z"),
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if got.Requests != 1 || got.Bytes != 5 {
		t.Fatalf("totals = %d/%d, want 1/5", got.Requests, got.Bytes)
	}
}

func TestAggregateUserDisplayName(t *testing.T) {
	agg := New(Config{})
	logs := []model.RequestLog{
		logRow("2026-10-01T01:00:00Z", "u1", "", "acct-a", "h.example", 200, 1, 1),
		logRow("2026-10-01T02:00:00Z", "u1", "ada", "acct-a", "h.example", 200, 1, 1),
	}
	got, err := agg.Aggregate(logs, Query{
		From: mustTime(t, "2026-10-01T00:00:00Z"),
		To:   mustTime(t, "2026-10-01T23:59:59Z"),
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if len(got.Users) != 1 || got.Users[0].UserName != "ada" {
		t.Fatalf("Users = %+v, want the first non-empty display name", got.Users)
	}
}

func TestAggregateRowIDsAreNotRequired(t *testing.T) {
	agg := New(Config{})
	logs := []model.RequestLog{{TS: "2026-10-01T01:00:00Z", Bytes: 1}}
	got, err := agg.Aggregate(logs, Query{
		From: mustTime(t, "2026-10-01T00:00:00Z"),
		To:   mustTime(t, "2026-10-01T23:59:59Z"),
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if got.Requests != 1 || len(got.Users) != 1 || got.Users[0].UserID != "" {
		t.Fatalf("summary = %+v", got)
	}
}

// TestRetentionFields pins the honest-history contract: rowsRetained is the
// configured store cap (independent of the window and filters) and
// retentionDays counts the retained span from the oldest row through now.
func TestRetentionFields(t *testing.T) {
	agg := New(Config{
		Now:           func() time.Time { return testNow },
		RetentionRows: 5000,
	})
	logs := []model.RequestLog{
		logRow("2026-10-01T05:00:00Z", "u1", "ada", "acct-a", "old.example:443", 200, 10, 1),
		logRow("2026-10-24T09:00:00Z", "u1", "ada", "acct-a", "today.example:443", 200, 20, 2),
		logRow("not-a-time", "u1", "ada", "acct-a", "bad.example:443", 200, 30, 3),
	}
	got, err := agg.Aggregate(logs, Query{
		From: mustTime(t, "2026-10-24T00:00:00Z"),
		To:   mustTime(t, "2026-10-24T23:59:59Z"),
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if got.RowsRetained != 5000 {
		t.Fatalf("RowsRetained = %d, want the configured 5000", got.RowsRetained)
	}
	// 2026-10-01 through 2026-10-24 inclusive.
	if got.RetentionDays != 24 {
		t.Fatalf("RetentionDays = %d, want 24", got.RetentionDays)
	}
	if got.Requests != 1 || got.Bytes != 20 {
		t.Fatalf("window totals = %d/%d, want 1/20 (retention must not widen the window)", got.Requests, got.Bytes)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"rowsRetained":5000`, `"retentionDays":24`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("summary JSON missing %s: %s", want, encoded)
		}
	}

	empty, err := New(Config{Now: func() time.Time { return testNow }}).Aggregate(nil, Query{
		From: mustTime(t, "2026-10-24T00:00:00Z"),
		To:   mustTime(t, "2026-10-24T23:59:59Z"),
	})
	if err != nil {
		t.Fatalf("Aggregate empty: %v", err)
	}
	if empty.RowsRetained != DefaultRetentionRows || empty.RetentionDays != 0 {
		t.Fatalf("empty retention = %d rows/%d days, want %d/0",
			empty.RowsRetained, empty.RetentionDays, DefaultRetentionRows)
	}
}

func TestAggregateReportsQuotaEnforcedContract(t *testing.T) {
	agg := New(Config{Now: func() time.Time { return testNow }})
	summary, err := agg.Aggregate(nil, Query{})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	// The gate has no quota logic yet, so the wire field must be present and
	// false rather than omitted; the panel keys its advisory label off it.
	if summary.QuotaEnforced {
		t.Fatal("QuotaEnforced = true, but the gate does not enforce quotas")
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"quotaEnforced":false`) {
		t.Fatalf("summary JSON omits quotaEnforced:false: %s", encoded)
	}
}
