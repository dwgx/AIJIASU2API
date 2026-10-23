package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// TestRequestsDefaultPageSize is the bench regression: GET /v1/requests was
// the only list route without a page size, so the seeded 40 rows answered
// 149.8 kB per request (BENCH.md §6.5). The default page is now bounded while
// an explicit ?limit= (including ?limit=0 for every row) still wins.
func TestRequestsDefaultPageSize(t *testing.T) {
	st := store.New()
	const extra = 250
	for i := range extra {
		if _, err := st.CreateMutation(model.MutationLabInput{
			NodeID: "node-01",
			Method: "GET",
			Target: "example.com:443",
		}); err != nil {
			t.Fatalf("CreateMutation %d: %v", i, err)
		}
	}
	total := len(st.Requests())
	if total <= 200 {
		t.Fatalf("seeded requests = %d, want more than the default page size", total)
	}

	server := New(Config{AdminToken: testToken}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/requests", nil)
	requireStatus(t, resp, http.StatusOK)
	rows := decodeBody[[]model.RequestLog](t, body)
	if len(rows) != 200 {
		t.Fatalf("default page = %d rows, want 200", len(rows))
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/requests?limit=20", nil)
	requireStatus(t, resp, http.StatusOK)
	rows = decodeBody[[]model.RequestLog](t, body)
	if len(rows) != 20 {
		t.Fatalf("?limit=20 = %d rows, want 20", len(rows))
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/requests?limit=0", nil)
	requireStatus(t, resp, http.StatusOK)
	rows = decodeBody[[]model.RequestLog](t, body)
	if len(rows) != total {
		t.Fatalf("?limit=0 = %d rows, want all %d", len(rows), total)
	}

	resp, body = apiCall(t, ts, http.MethodGet, "/v1/requests?offset=250", nil)
	requireStatus(t, resp, http.StatusOK)
	rows = decodeBody[[]model.RequestLog](t, body)
	if want := total - 250; len(rows) != want {
		t.Fatalf("?offset=250 = %d rows, want %d", len(rows), want)
	}
}
