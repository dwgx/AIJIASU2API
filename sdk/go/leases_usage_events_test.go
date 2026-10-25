package ajs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestListLeasesQuery(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/leases" || q.Get("user_id") != "usr-1" ||
			q.Get("limit") != "20" || q.Get("offset") != "40" {
			t.Errorf("path/query = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		writeJSON(w, 200, `[{"id":"lease-1","userId":"usr-1","userName":"u","accountId":"acc-1",
			"accountLabel":"primary","platform":"linux","nodeId":"acc-1:srv-1","nodeLabel":"Tokyo",
			"region":"jp","egressIp":"203.0.113.9","protocol":"socks5","gatePort":19001,
			"ttlSeconds":3600,"expiresAt":"2026-10-26T01:00:00Z","requests":3,"bytes":1024,
			"sticky":true,"status":"active","idempotencyKey":"k","createdAt":"2026-10-26T00:00:00Z"}]`)
	})
	leases, err := client.ListLeases(context.Background(), ListLeasesOptions{
		UserID: "usr-1", Limit: 20, Offset: 40,
	})
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if len(leases) != 1 || leases[0].ID != "lease-1" || leases[0].Bytes != 1024 {
		t.Fatalf("leases = %+v", leases)
	}
}

func TestRotateLease(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/leases/lease-1/actions/rotate" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, 200, `{"id":"lease-1","status":"active","egressIp":"203.0.113.10"}`)
	})
	lease, err := client.RotateLease(context.Background(), "lease-1")
	if err != nil {
		t.Fatalf("RotateLease: %v", err)
	}
	if lease.EgressIP != "203.0.113.10" {
		t.Fatalf("lease = %+v", lease)
	}
}

func TestReleaseLeaseConfirms(t *testing.T) {
	var confirm bool
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/leases/lease-1" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body RevokeLeaseInput
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		confirm = body.Confirm
		w.WriteHeader(http.StatusNoContent)
	})
	if err := client.ReleaseLease(context.Background(), "lease-1"); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
	if !confirm {
		t.Fatal("confirm was not sent")
	}
}

func TestGetUsageQueryAndDecode(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/usage" || q.Get("from") != "2026-10-01" ||
			q.Get("to") != "2026-10-26" || q.Get("account") != "primary" ||
			q.Get("userId") != "usr-1" {
			t.Errorf("path/query = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		writeJSON(w, 200, `{"from":"2026-10-01T00:00:00Z","to":"2026-10-26T23:59:59Z",
			"requests":10,"bytes":2048,"errors":1,
			"accounts":[{"account":"primary","requests":10,"bytes":2048,"errors":1,
			"avgLatencyMs":12,"buckets":[],"trafficBudgetBytes":null,"usedBytes":100,
			"remainingBytes":null}],
			"users":[],"days":[],"topTargets":[],"truncated":false,"rowsRetained":10000,
			"retentionDays":3,"quotaEnforced":false}`)
	})
	summary, err := client.GetUsage(context.Background(), UsageOptions{
		From: "2026-10-01", To: "2026-10-26", Account: "primary", UserID: "usr-1",
	})
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}
	if summary.Requests != 10 || summary.Bytes != 2048 || len(summary.Accounts) != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.Accounts[0].TrafficBudgetBytes != nil || summary.Accounts[0].RemainingBytes != nil {
		t.Fatalf("nullable budgets decoded incorrectly: %+v", summary.Accounts[0])
	}
}

func TestListEventsQueryAndWait(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/events" || q.Get("since") != "evt-000010" ||
			q.Get("limit") != "50" || q.Get("wait") != "25s" {
			t.Errorf("path/query = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		writeJSON(w, 200, `{"events":[{"id":"evt-000011","type":"lease.created",
			"ts":"2026-10-26T00:00:00Z","data":{"leaseId":"lease-1"}}],
			"nextCursor":"evt-000011","oldestCursor":"evt-000001","dropped":0}`)
	})
	page, err := client.ListEvents(context.Background(), ListEventsOptions{
		Since: "evt-000010", Limit: 50, Wait: 25 * time.Second,
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(page.Events) != 1 || page.Events[0].ID != "evt-000011" ||
		page.Events[0].Data["leaseId"] != "lease-1" || page.NextCursor != "evt-000011" {
		t.Fatalf("page = %+v", page)
	}
}

func TestListEventsCursorExpired(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusGone, `{"error":{"code":"CURSOR_EXPIRED",
			"message":"cursor is no longer retained","details":{"oldestCursor":"evt-000042"}}}`)
	})
	_, err := client.ListEvents(context.Background(), ListEventsOptions{Since: "evt-000001"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "CURSOR_EXPIRED" ||
		apiErr.Details["oldestCursor"] != "evt-000042" {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamEventsParsesFrames(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events/stream" || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("request = %s accept=%q", r.URL.Path, r.Header.Get("Accept"))
		}
		if got := r.Header.Get("Last-Event-ID"); got != "evt-000009" {
			t.Errorf("Last-Event-ID = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, ": keepalive\n\n")
		_, _ = io.WriteString(w, "id: evt-000010\nevent: lease.created\ndata: {\"id\":\"evt-000010\",\n")
		_, _ = io.WriteString(w, "data: \"type\":\"lease.created\",\"ts\":\"2026-10-26T00:00:00Z\",\n")
		_, _ = io.WriteString(w, "data: \"data\":{\"leaseId\":\"lease-1\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "id: evt-000011\nevent: lease.revoked\r\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"evt-000011\",\"type\":\"lease.revoked\",\r\n")
		_, _ = io.WriteString(w, "data: \"ts\":\"2026-10-26T00:01:00Z\",\"data\":{}}\r\n\r\n")
	})
	stream, err := client.StreamEvents(context.Background(), StreamEventsOptions{
		Since: "evt-000009", LastEventID: "evt-000009",
	})
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}
	defer stream.Close()
	first, err := stream.Next()
	if err != nil {
		t.Fatalf("Next 1: %v", err)
	}
	if first.ID != "evt-000010" || first.Type != "lease.created" ||
		first.Data["leaseId"] != "lease-1" {
		t.Fatalf("first = %+v", first)
	}
	second, err := stream.Next()
	if err != nil {
		t.Fatalf("Next 2: %v", err)
	}
	if second.ID != "evt-000011" || second.Type != "lease.revoked" {
		t.Fatalf("second = %+v", second)
	}
	if _, err := stream.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next 3 err = %v, want io.EOF", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestFollowEvents(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 2 {
			id := "evt-" + strconv.Itoa(i+1)
			fmt.Fprintf(w, "id: %s\nevent: test\ndata: {\"id\":%q,\"type\":\"test\",\"ts\":\"\",\"data\":{}}\n\n", id, id)
		}
	})
	var seen atomic.Int32
	err := client.FollowEvents(context.Background(), StreamEventsOptions{}, func(Event) error {
		seen.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("FollowEvents: %v", err)
	}
	if seen.Load() != 2 {
		t.Fatalf("seen = %d, want 2", seen.Load())
	}
}

func TestStreamEventsAuthErrorLeaksNoToken(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, `{"error":{"code":"UNAUTHORIZED","message":"bad token"}}`)
	})
	_, err := client.StreamEvents(context.Background(), StreamEventsOptions{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "UNAUTHORIZED" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatal("stream error leaked the token")
	}
}
