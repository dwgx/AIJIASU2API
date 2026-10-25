package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

func newDebugServer(t *testing.T, capacity int) (*httptest.Server, *runtime.DebugRecorder) {
	t.Helper()
	rec := runtime.NewDebugRecorder(capacity)
	t.Cleanup(rec.Close)
	server := New(Config{AdminToken: testToken, Debug: rec}, store.New(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, rec
}

func TestDebugTraceReturnsAttributedRedactedRing(t *testing.T) {
	ts, rec := newDebugServer(t, 0)
	rec.Record(model.DebugTrace{
		Kind: model.DebugKindSwitchStep, AccountID: "acc-01", NodeID: "acc-01#1:s1",
		ServerID: "s1", Slot: 1, Generation: 3, Step: "open_session", State: model.DebugStateOK,
		DurationMs: 42,
	})
	rec.Record(model.DebugTrace{
		Kind: model.DebugKindGateOpen, AccountID: "acc-01", NodeID: "acc-01#1:s1",
		ServerID: "s1", Slot: 1, Generation: 3, State: model.DebugStateOpen,
	})
	rec.Record(model.DebugTrace{
		Kind: model.DebugKindSessionLost, AccountID: "acc-01", NodeID: "acc-01#1:s1",
		ServerID: "s1", Slot: 1, Generation: 3, State: model.DebugStateLost,
		Detail: "cookie=secret-cookie sid=secret-sid token=secret-token device_id=secret-device cuid=secret-cuid",
	})
	rec.Record(model.DebugTrace{
		Kind: model.DebugKindLeaseCreate, AccountID: "acc-01", NodeID: "acc-01#1:s1",
		Slot: 1, Generation: 3, State: "active", Detail: "lease lease-01",
	})
	rec.Record(model.DebugTrace{
		Kind: model.DebugKindSlotState, AccountID: "acc-02", NodeID: "acc-02#1:s2",
		ServerID: "s2", Slot: 1, Generation: 1, State: string(model.SlotDown),
	})

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/debug/trace", nil)
	requireStatus(t, resp, http.StatusOK)
	page := decodeBody[model.DebugTracePage](t, body)
	if !page.Enabled || page.Capacity != runtime.DebugRingCapacity {
		t.Fatalf("page = %+v", page)
	}
	if len(page.Entries) != 5 || page.NextSeq != 5 || page.OldestSeq != 1 {
		t.Fatalf("entries = %d next=%d oldest=%d", len(page.Entries), page.NextSeq, page.OldestSeq)
	}
	for i := 1; i < len(page.Entries); i++ {
		if page.Entries[i].Seq <= page.Entries[i-1].Seq {
			t.Fatalf("entries are not ordered at %d: %d then %d", i, page.Entries[i-1].Seq, page.Entries[i].Seq)
		}
	}
	kinds := map[string]bool{}
	for _, entry := range page.Entries {
		kinds[entry.Kind] = true
		if entry.Slot == 0 || entry.Generation == 0 || entry.NodeID == "" {
			t.Fatalf("entry lacks slot/generation/node attribution: %+v", entry)
		}
	}
	for _, kind := range []string{
		model.DebugKindSwitchStep, model.DebugKindGateOpen, model.DebugKindSessionLost,
		model.DebugKindLeaseCreate, model.DebugKindSlotState,
	} {
		if !kinds[kind] {
			t.Fatalf("missing kind %q (have %v)", kind, kinds)
		}
	}
	if page.Entries[0].DurationMs != 42 || page.Entries[0].Step != "open_session" {
		t.Fatalf("first entry lost its timing/step: %+v", page.Entries[0])
	}
	for _, secret := range []string{"secret-cookie", "secret-sid", "secret-token", "secret-device", "secret-cuid"} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("trace body leaked %q: %s", secret, body)
		}
	}
	if !bytes.Contains(body, []byte("[REDACTED]")) {
		t.Fatalf("trace body has no redaction marker: %s", body)
	}

	// ?account= filters; ?limit= keeps the newest window; a bad limit is 400.
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/debug/trace?account=acc-01", nil)
	requireStatus(t, resp, http.StatusOK)
	filtered := decodeBody[model.DebugTracePage](t, body)
	if len(filtered.Entries) != 4 || filtered.Retained != 4 {
		t.Fatalf("account filter = %d entries (retained %d), want 4", len(filtered.Entries), filtered.Retained)
	}
	for _, entry := range filtered.Entries {
		if entry.AccountID != "acc-01" {
			t.Fatalf("account filter returned %q", entry.AccountID)
		}
	}
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/debug/trace?limit=2", nil)
	requireStatus(t, resp, http.StatusOK)
	limited := decodeBody[model.DebugTracePage](t, body)
	if len(limited.Entries) != 2 || limited.Entries[1].Seq != 5 {
		t.Fatalf("limit window = %+v", limited.Entries)
	}
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/debug/trace?limit=-1", nil)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
}

func TestDebugTraceScopeMatrix(t *testing.T) {
	ts, rec := newDebugServer(t, 8)
	rec.Record(model.DebugTrace{Kind: model.DebugKindSwitch, AccountID: "acc-01", Slot: 1, Generation: 1, NodeID: "acc-01#1:s1"})

	resp, _ := apiCall(t, ts, http.MethodGet, "/v1/debug/trace", nil)
	requireStatus(t, resp, http.StatusOK)

	catalogRead, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01", "label": "debug reader", "scopes": []string{"catalog:read"},
	})
	resp, body := scopedCall(t, ts, catalogRead.Secret, http.MethodGet, "/v1/debug/trace", nil)
	requireStatus(t, resp, http.StatusOK)
	if len(decodeBody[model.DebugTracePage](t, body).Entries) != 1 {
		t.Fatalf("catalog:read page = %s", body)
	}
	// A stream never ends on its own, so read only the response headers and
	// close the body instead of the doRequest helper's io.ReadAll.
	streamCtx, streamCancel := context.WithCancel(context.Background())
	streamReq := newRequest(t, http.MethodGet, ts.URL+"/v1/debug/stream", nil).WithContext(streamCtx)
	streamReq.Header.Set("Authorization", "Bearer "+catalogRead.Secret)
	streamResp, err := ts.Client().Do(streamReq)
	if err != nil {
		streamCancel()
		t.Fatalf("catalog:read stream: %v", err)
	}
	if streamResp.StatusCode != http.StatusOK {
		streamCancel()
		streamResp.Body.Close()
		t.Fatalf("catalog:read stream status = %d, want 200", streamResp.StatusCode)
	}
	streamCancel()
	streamResp.Body.Close()

	for _, scopes := range [][]string{{"catalog:write"}, {"accounts:read"}, {"events:read"}, {"leases:read"}} {
		key, _ := mustCreateScopedKey(t, ts, map[string]any{
			"userId": "u-01", "label": "wrong scope", "scopes": scopes,
		})
		resp, body := scopedCall(t, ts, key.Secret, http.MethodGet, "/v1/debug/trace", nil)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorCode(t, body, CodeForbidden)
		resp, body = scopedCall(t, ts, key.Secret, http.MethodGet, "/v1/debug/stream", nil)
		requireStatus(t, resp, http.StatusForbidden)
		requireErrorCode(t, body, CodeForbidden)
	}
}

func TestDebugTraceRestrictedKeySeesOnlyItsAccounts(t *testing.T) {
	ts, rec := newDebugServer(t, 8)
	rec.Record(model.DebugTrace{Kind: model.DebugKindSwitch, AccountID: "acc-01", Slot: 1, Generation: 1, NodeID: "acc-01#1:s1"})
	rec.Record(model.DebugTrace{Kind: model.DebugKindSwitch, AccountID: "acc-02", Slot: 1, Generation: 1, NodeID: "acc-02#1:s1"})

	key, _ := mustCreateScopedKey(t, ts, map[string]any{
		"userId": "u-01", "label": "restricted debug reader",
		"scopes": []string{"catalog:read"}, "accountIds": []string{"acc-01"},
	})
	resp, body := scopedCall(t, ts, key.Secret, http.MethodGet, "/v1/debug/trace", nil)
	requireStatus(t, resp, http.StatusOK)
	page := decodeBody[model.DebugTracePage](t, body)
	if len(page.Entries) != 1 || page.Entries[0].AccountID != "acc-01" {
		t.Fatalf("restricted page = %+v", page.Entries)
	}
	resp, body = scopedCall(t, ts, key.Secret, http.MethodGet, "/v1/debug/trace?account=acc-02", nil)
	requireStatus(t, resp, http.StatusForbidden)
	requireErrorCode(t, body, CodeForbidden)
	resp, _ = scopedCall(t, ts, key.Secret, http.MethodGet, "/v1/debug/trace?account=acc-01", nil)
	requireStatus(t, resp, http.StatusOK)
}

func TestDebugStreamReplaysThenStreamsLive(t *testing.T) {
	ts, rec := newDebugServer(t, 8)
	rec.Record(model.DebugTrace{Kind: model.DebugKindGateOpen, AccountID: "acc-01", Slot: 1, Generation: 1, NodeID: "acc-01#1:s1"})
	rec.Record(model.DebugTrace{Kind: model.DebugKindGateClose, AccountID: "acc-01", Slot: 1, Generation: 1, NodeID: "acc-01#1:s1"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := newRequest(t, http.MethodGet, ts.URL+"/v1/debug/stream?since=1", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream status = %d (body %s)", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}

	reader := bufio.NewReader(resp.Body)
	first := readDebugFrame(t, reader)
	if first.Seq != 2 || first.Kind != model.DebugKindGateClose {
		t.Fatalf("first frame = %+v, want the replayed gate.close at seq 2", first)
	}
	rec.Record(model.DebugTrace{Kind: model.DebugKindSessionOpen, AccountID: "acc-01", Slot: 1, Generation: 2, NodeID: "acc-01#1:s1"})
	second := readDebugFrame(t, reader)
	if second.Seq != 3 || second.Kind != model.DebugKindSessionOpen {
		t.Fatalf("second frame = %+v, want the live session.open at seq 3", second)
	}

	// A Last-Event-ID header wins over ?since and resumes after that sequence.
	cancel()
	req = newRequest(t, http.MethodGet, ts.URL+"/v1/debug/stream?since=0", nil).WithContext(context.Background())
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Last-Event-ID", "evt-000002")
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatalf("open resumed stream: %v", err)
	}
	defer resp.Body.Close()
	resumed := readDebugFrame(t, bufio.NewReader(resp.Body))
	if resumed.Seq != 3 {
		t.Fatalf("resumed frame = %+v, want seq 3", resumed)
	}
}

func TestDebugStreamRestartsOnStaleCursor(t *testing.T) {
	ts, rec := newDebugServer(t, 2)
	rec.Record(model.DebugTrace{Kind: model.DebugKindSwitch, AccountID: "acc-01"})
	rec.Record(model.DebugTrace{Kind: model.DebugKindSwitch, AccountID: "acc-01"})
	rec.Record(model.DebugTrace{Kind: model.DebugKindSwitch, AccountID: "acc-01"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := newRequest(t, http.MethodGet, ts.URL+"/v1/debug/stream?since=1", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Debug-Restarted"); got != "true" {
		t.Fatalf("X-Debug-Restarted = %q, want true", got)
	}
	reader := bufio.NewReader(resp.Body)
	first := readDebugFrame(t, reader)
	if first.Seq != 2 {
		t.Fatalf("restarted first frame = %+v, want seq 2 (oldest retained)", first)
	}
	second := readDebugFrame(t, reader)
	if second.Seq != 3 {
		t.Fatalf("restarted second frame = %+v, want seq 3", second)
	}

	// A cursor ahead of this process's sequence (a previous process) must also
	// restart instead of skipping the whole retained window.
	cancel()
	resp.Body.Close()
	req = newRequest(t, http.MethodGet, ts.URL+"/v1/debug/stream?since=99", nil).WithContext(context.Background())
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatalf("open future-cursor stream: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Debug-Restarted"); got != "true" {
		t.Fatalf("future cursor X-Debug-Restarted = %q, want true", got)
	}
	future := readDebugFrame(t, bufio.NewReader(resp.Body))
	if future.Seq != 2 {
		t.Fatalf("future-cursor first frame = %+v, want seq 2 (oldest retained)", future)
	}
}

func TestLeaseTransitionsEmitDebugTrace(t *testing.T) {
	rec := runtime.NewDebugRecorder(8)
	fixture := newLeaseHTTPServer(t, Config{Debug: rec})
	// The shared fixture predates multi-slot; stamp one LIVE slot so the lease
	// carries the slot/generation attribution the debugger contract requires.
	slotKey := model.SlotKey{AccountID: fixture.account.ID, Index: 1}
	if err := fixture.st.UpsertSlot(model.AccountSlot{
		SlotKey: slotKey, State: model.SlotLive, Generation: 1, GatePort: 13001,
	}); err != nil {
		t.Fatalf("UpsertSlot: %v", err)
	}
	for _, id := range []string{"node-live", "node-live-2"} {
		node, ok := fixture.st.Node(id)
		if !ok {
			t.Fatalf("node %q is missing", id)
		}
		node.Slot = 1
		if _, err := fixture.st.PutNode(node); err != nil {
			t.Fatalf("PutNode(%q): %v", id, err)
		}
	}
	resp, body := apiCall(t, fixture.ts, http.MethodPost, "/v1/leases", map[string]any{
		"userId": fixture.user.ID, "platform": "android", "accountId": fixture.account.ID,
	})
	requireStatus(t, resp, http.StatusCreated)
	created := decodeBody[leaseCreateResult](t, body).Lease
	if created.ID == "" {
		t.Fatalf("created lease = %+v", created)
	}
	resp, _ = apiCall(t, fixture.ts, http.MethodDelete, "/v1/leases/"+created.ID, map[string]any{"confirm": true})
	requireStatus(t, resp, http.StatusNoContent)

	page := rec.Snapshot(fixture.account.ID, 0)
	createdEntries := debugKindEntries(page.Entries, model.DebugKindLeaseCreate)
	revokedEntries := debugKindEntries(page.Entries, model.DebugKindLeaseRevoke)
	if len(createdEntries) != 1 || len(revokedEntries) != 1 {
		t.Fatalf("lease trace = create %d revoke %d (entries %+v)", len(createdEntries), len(revokedEntries), page.Entries)
	}
	for _, entry := range []model.DebugTrace{createdEntries[0], revokedEntries[0]} {
		if entry.AccountID != fixture.account.ID || entry.NodeID == "" || entry.Slot == 0 || entry.Generation == 0 {
			t.Fatalf("lease trace attribution = %+v", entry)
		}
	}
	if createdEntries[0].Detail != "lease "+created.ID {
		t.Fatalf("lease create detail = %q, want the lease id", createdEntries[0].Detail)
	}
}

func debugKindEntries(entries []model.DebugTrace, kind string) []model.DebugTrace {
	out := make([]model.DebugTrace, 0, len(entries))
	for _, entry := range entries {
		if entry.Kind == kind {
			out = append(out, entry)
		}
	}
	return out
}

func TestDebugTraceUnavailableWithoutRecorder(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/debug/trace", nil)
	requireStatus(t, resp, http.StatusServiceUnavailable)
	requireErrorCode(t, body, CodeUpstreamUnavailable)
	resp, body = apiCall(t, ts, http.MethodGet, "/v1/debug/stream", nil)
	requireStatus(t, resp, http.StatusServiceUnavailable)
	requireErrorCode(t, body, CodeUpstreamUnavailable)
}

// readDebugFrame reads one id/event/data frame and decodes the trace JSON.
func readDebugFrame(t *testing.T, reader *bufio.Reader) model.DebugTrace {
	t.Helper()
	frame := readSSEFrame(t, reader)
	var trace model.DebugTrace
	if err := json.Unmarshal([]byte(frame.data), &trace); err != nil {
		t.Fatalf("decode SSE data %q: %v", frame.data, err)
	}
	if frame.id != strconv.FormatUint(trace.Seq, 10) {
		t.Fatalf("frame id = %q, trace seq = %d", frame.id, trace.Seq)
	}
	if frame.event != trace.Kind {
		t.Fatalf("frame event = %q, trace kind = %q", frame.event, trace.Kind)
	}
	return trace
}
