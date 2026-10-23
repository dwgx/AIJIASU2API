package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/api"
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/publisher"
	"ajsaccountd/internal/resin"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/store"
)

// newStartedPanel starts a panel against the seed store and clears the calls
// recorded by Start, so mutation tests assert only their own traffic.
func newStartedPanel(t *testing.T) (*store.Store, *runtime.ResinPanel, *fakeResin) {
	t.Helper()
	st := store.New()
	panel, fake, _ := newTestPanel(t, st)
	if err := panel.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fake.reset()
	return st, panel, fake
}

func requireCalls(t *testing.T, fake *fakeResin, want []string) {
	t.Helper()
	got := fake.paths()
	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

// filterPatches returns the regex_filters sent by every PATCH platform call.
func filterPatches(t *testing.T, fake *fakeResin, platformPath string) [][]string {
	t.Helper()
	out := make([][]string, 0, 2)
	for _, call := range fake.recorded() {
		if call.Method != http.MethodPatch || call.Path != platformPath {
			continue
		}
		var patch resin.UpdatePlatformRequest
		if err := json.Unmarshal(call.Body, &patch); err != nil {
			t.Fatalf("decode platform patch %s: %v", call.Body, err)
		}
		if patch.RegexFilters == nil {
			t.Fatalf("platform patch has no regex_filters: %s", call.Body)
		}
		out = append(out, append([]string(nil), (*patch.RegexFilters)...))
	}
	return out
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestRotateLeaseThroughPanel(t *testing.T) {
	st, panel, fake := newStartedPanel(t)
	fake.setLease("acc-01", resin.Lease{
		PlatformID: "plat-1",
		Account:    "acc-01",
		NodeHash:   "hash-01",
		NodeTag:    "ajs-live/cn-zj-01",
		EgressIP:   "203.0.113.99",
		Expiry:     "2026-10-23T13:00:00Z",
	})
	before, ok := st.Lease("lease-01")
	if !ok {
		t.Fatal("seed lease-01 missing")
	}

	updated, err := panel.RotateLease(context.Background(), "lease-01")
	if err != nil {
		t.Fatalf("RotateLease: %v", err)
	}

	// The publisher reads the filters, excludes the current full Resin tag,
	// deletes the lease and restores the filters. The panel then reads the new
	// lease; the publisher restores before returning, so the GET is last.
	requireCalls(t, fake, []string{
		"GET /api/v1/platforms/plat-1",
		"PATCH /api/v1/platforms/plat-1",
		"DELETE /api/v1/platforms/plat-1/leases/acc-01",
		"PATCH /api/v1/platforms/plat-1",
		"GET /api/v1/platforms/plat-1/leases/acc-01",
	})

	patches := filterPatches(t, fake, "/api/v1/platforms/plat-1")
	if len(patches) != 2 {
		t.Fatalf("filter patches = %v, want exclude + restore", patches)
	}
	if !hasString(patches[0], "!^ajs-live/cn-zj-01$") {
		t.Fatalf("exclusion patch = %v, want !^ajs-live/cn-zj-01$", patches[0])
	}
	if len(patches[1]) != 1 || patches[1][0] != "ajs-live/.*" {
		t.Fatalf("restore patch = %v, want [ajs-live/.*]", patches[1])
	}
	if got := fake.platformValue().RegexFilters; len(got) != 1 || got[0] != "ajs-live/.*" {
		t.Fatalf("platform filters after rotate = %v, want restored", got)
	}

	if updated.EgressIP != "203.0.113.99" || updated.ExpiresAt != "2026-10-23T13:00:00Z" {
		t.Fatalf("rotated lease egress/expiry = %q/%q", updated.EgressIP, updated.ExpiresAt)
	}
	if updated.ID != before.ID || updated.UserID != before.UserID ||
		updated.UserName != before.UserName || updated.AccountID != before.AccountID ||
		updated.AccountLabel != before.AccountLabel || updated.NodeID != before.NodeID ||
		updated.NodeLabel != before.NodeLabel || updated.Region != before.Region ||
		updated.Protocol != before.Protocol || updated.GatePort != before.GatePort ||
		updated.TTLSeconds != before.TTLSeconds || updated.Requests != before.Requests ||
		updated.Bytes != before.Bytes || updated.Sticky != before.Sticky ||
		updated.CreatedAt != before.CreatedAt {
		t.Fatalf("rotate changed immutable fields:\nbefore %+v\nafter  %+v", before, updated)
	}

	stored, ok := st.Lease("lease-01")
	if !ok || stored.EgressIP != "203.0.113.99" || stored.ExpiresAt != "2026-10-23T13:00:00Z" {
		t.Fatalf("store lease after rotate = %+v (ok=%v)", stored, ok)
	}
	node, ok := st.Node("node-01")
	if !ok || node.EgressIP != "203.0.113.99" {
		t.Fatalf("node-01 egress after rotate = %q (ok=%v), want 203.0.113.99", node.EgressIP, ok)
	}
}

func TestReconcileWaitsForRotation(t *testing.T) {
	_, panel, fake := newStartedPanel(t)
	fake.setLease("acc-01", resin.Lease{
		PlatformID: "plat-1",
		Account:    "acc-01",
		EgressIP:   "203.0.113.99",
		Expiry:     "2026-10-23T13:00:00Z",
	})
	started := make(chan struct{})
	release := make(chan struct{})
	fake.mu.Lock()
	fake.leaseDeleteStarted = started
	fake.leaseDeleteRelease = release
	fake.mu.Unlock()
	fake.reset()

	rotateDone := make(chan error, 1)
	go func() {
		_, err := panel.RotateLease(context.Background(), "lease-01")
		rotateDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("rotation never reached DeleteLease")
	}

	reconcileDone := make(chan error, 1)
	go func() {
		_, err := panel.Reconcile(context.Background())
		reconcileDone <- err
	}()

	// With RotateLease holding opMu, Reconcile cannot run while the exclusion
	// is live: no platform PATCH beyond the exclusion may appear, and the
	// reconcile must not finish.
	select {
	case err := <-reconcileDone:
		t.Fatalf("Reconcile finished during rotation: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if got := fake.countExact(http.MethodPatch, "/api/v1/platforms/plat-1"); got != 1 {
		t.Fatalf("platform patches during rotation = %d, want 1 (the exclusion): %v", got, fake.paths())
	}

	close(release)
	if err := <-rotateDone; err != nil {
		t.Fatalf("RotateLease: %v", err)
	}
	if err := <-reconcileDone; err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := fake.platformValue().RegexFilters; len(got) != 1 || got[0] != "ajs-live/.*" {
		t.Fatalf("platform filters after both = %v, want the original list", got)
	}
}

func TestRotateLeaseErrors(t *testing.T) {
	t.Run("missing lease", func(t *testing.T) {
		_, panel, fake := newStartedPanel(t)
		_, err := panel.RotateLease(context.Background(), "lease-99")
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("error = %v, want store.ErrNotFound", err)
		}
		if calls := fake.recorded(); len(calls) != 0 {
			t.Fatalf("missing lease made Resin calls: %v", fake.paths())
		}
	})

	t.Run("missing platform", func(t *testing.T) {
		st := store.New()
		panel, _, _ := newTestPanel(t, st) // not started: no platform id
		_, err := panel.RotateLease(context.Background(), "lease-01")
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("error = %v, want store.ErrConflict", err)
		}
	})

	t.Run("tag no longer published", func(t *testing.T) {
		_, panel, _ := newStartedPanel(t)
		// Draining through the panel removes node-06 from the topology; a
		// lease still pointing at it cannot use filter exclusion.
		if _, err := panel.DrainNode(context.Background(), "node-06"); err != nil {
			t.Fatalf("DrainNode: %v", err)
		}
		_, err := panel.RotateLease(context.Background(), "lease-02")
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("error = %v, want store.ErrConflict", err)
		}
	})

	t.Run("rate limited", func(t *testing.T) {
		st, panel, fake := newStartedPanel(t)
		fake.setLease("acc-01", resin.Lease{EgressIP: "203.0.113.99", Expiry: "2026-10-23T13:00:00Z"})
		if _, err := panel.RotateLease(context.Background(), "lease-01"); err != nil {
			t.Fatalf("first rotate: %v", err)
		}
		fake.reset()

		_, err := panel.RotateLease(context.Background(), "lease-01")
		if !errors.Is(err, runtime.ErrRotationRateLimited) {
			t.Fatalf("error = %v, want ErrRotationRateLimited", err)
		}
		if !errors.Is(err, publisher.ErrRotationRateLimit) {
			t.Fatalf("error = %v, want it to match publisher.ErrRotationRateLimit", err)
		}
		if calls := fake.recorded(); len(calls) != 0 {
			t.Fatalf("rate-limited rotate made Resin calls: %v", fake.paths())
		}

		// The API maps the window to 409 CONFLICT.
		ts := httptest.NewServer(api.New(api.Config{AllowInsecure: true, Panel: panel}, st, discardLogger()))
		t.Cleanup(ts.Close)
		status, body := apiCall(t, ts, http.MethodPost, "/v1/leases/lease-01/actions/rotate")
		if status != http.StatusConflict {
			t.Fatalf("rotate rate limit = %d, want 409 (body %s)", status, body)
		}
		if code := errorCode(t, body); code != api.CodeConflict {
			t.Fatalf("rate limit code = %q, want %s", code, api.CodeConflict)
		}
		if !strings.Contains(string(body), "rate limited") {
			t.Fatalf("rate limit body = %s, want the publisher message", body)
		}
	})
}

func TestReleaseLeaseThroughPanel(t *testing.T) {
	st, panel, fake := newStartedPanel(t)

	if err := panel.ReleaseLease(context.Background(), "lease-01"); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
	requireCalls(t, fake, []string{"DELETE /api/v1/platforms/plat-1/leases/acc-01"})
	if _, ok := st.Lease("lease-01"); ok {
		t.Fatal("lease-01 still in the store after release")
	}
	if node, ok := st.Node("node-01"); !ok || node.InUse != 2 {
		t.Fatalf("node-01 inUse after release = %d (ok=%v), want 2", node.InUse, ok)
	}

	// A 404 from Resin means the lease is already gone; the store row must
	// still be removed and the call must succeed.
	fake.reset()
	fake.leaseDeleteStatus = http.StatusNotFound
	if err := panel.ReleaseLease(context.Background(), "lease-02"); err != nil {
		t.Fatalf("ReleaseLease with 404: %v", err)
	}
	requireCalls(t, fake, []string{"DELETE /api/v1/platforms/plat-1/leases/acc-03"})
	if _, ok := st.Lease("lease-02"); ok {
		t.Fatal("lease-02 still in the store after idempotent release")
	}

	// Missing store row is 404 without a Resin call.
	fake.reset()
	err := panel.ReleaseLease(context.Background(), "lease-99")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing lease error = %v, want store.ErrNotFound", err)
	}
	if calls := fake.recorded(); len(calls) != 0 {
		t.Fatalf("missing lease made Resin calls: %v", fake.paths())
	}
}

func TestDrainUndrainThroughPanel(t *testing.T) {
	_, panel, fake := newStartedPanel(t)

	node, err := panel.DrainNode(context.Background(), "node-06")
	if err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	if node.State != model.NodeDraining {
		t.Fatalf("drain state = %q, want DRAINING", node.State)
	}
	if got := fake.subscriptionContent(); got != wantContentAfterDrain {
		t.Fatalf("published content after drain =\n%s\nwant\n%s", got, wantContentAfterDrain)
	}
	topology, err := panel.Topology(context.Background())
	if err != nil {
		t.Fatalf("Topology: %v", err)
	}
	if _, ok := topology.Nodes["node-06"]; ok {
		t.Fatalf("drained node-06 still published: %v", topology.Nodes)
	}
	if len(topology.Nodes) != 7 {
		t.Fatalf("published nodes after drain = %d, want 7", len(topology.Nodes))
	}

	fake.reset()
	node, err = panel.UndrainNode(context.Background(), "node-06")
	if err != nil {
		t.Fatalf("UndrainNode: %v", err)
	}
	if node.State != model.NodeLive {
		t.Fatalf("undrain state = %q, want LIVE", node.State)
	}
	if got := fake.subscriptionContent(); got != wantContent {
		t.Fatalf("published content after undrain =\n%s\nwant\n%s", got, wantContent)
	}
	topology, err = panel.Topology(context.Background())
	if err != nil {
		t.Fatalf("Topology: %v", err)
	}
	if topology.Nodes["node-06"] != "hk-02" || len(topology.Nodes) != 8 {
		t.Fatalf("topology after undrain = %v, want node-06/hk-02 and 8 nodes", topology.Nodes)
	}
}

func TestDrainPublishFailureKeepsStoreState(t *testing.T) {
	t.Run("drain", func(t *testing.T) {
		st, panel, fake := newStartedPanel(t)
		fake.subscriptionPatchStatus = http.StatusInternalServerError

		ts := httptest.NewServer(api.New(api.Config{AllowInsecure: true, Panel: panel}, st, discardLogger()))
		t.Cleanup(ts.Close)
		status, body := apiCall(t, ts, http.MethodPost, "/v1/nodes/node-06/actions/drain")
		if status != http.StatusServiceUnavailable {
			t.Fatalf("drain with failing publish = %d, want 503 (body %s)", status, body)
		}
		if code := errorCode(t, body); code != api.CodeUpstreamUnavailable {
			t.Fatalf("drain error code = %q, want %s", code, api.CodeUpstreamUnavailable)
		}
		node, ok := st.Node("node-06")
		if !ok || node.State != model.NodeDraining {
			t.Fatalf("store node-06 after failed publish = %+v, want DRAINING", node)
		}
	})

	t.Run("undrain", func(t *testing.T) {
		st, panel, fake := newStartedPanel(t)
		if _, err := panel.DrainNode(context.Background(), "node-06"); err != nil {
			t.Fatalf("DrainNode: %v", err)
		}
		fake.subscriptionPatchStatus = http.StatusInternalServerError

		ts := httptest.NewServer(api.New(api.Config{AllowInsecure: true, Panel: panel}, st, discardLogger()))
		t.Cleanup(ts.Close)
		status, body := apiCall(t, ts, http.MethodPost, "/v1/nodes/node-06/actions/undrain")
		if status != http.StatusServiceUnavailable {
			t.Fatalf("undrain with failing publish = %d, want 503 (body %s)", status, body)
		}
		node, ok := st.Node("node-06")
		if !ok || node.State != model.NodeLive {
			t.Fatalf("store node-06 after failed undrain publish = %+v, want LIVE", node)
		}
	})
}

func TestAPIMutationsThroughPanel(t *testing.T) {
	st, panel, fake := newStartedPanel(t)
	fake.setLease("acc-01", resin.Lease{
		PlatformID: "plat-1",
		Account:    "acc-01",
		EgressIP:   "203.0.113.99",
		Expiry:     "2026-10-23T13:00:00Z",
	})
	ts := httptest.NewServer(api.New(api.Config{AllowInsecure: true, Panel: panel}, st, discardLogger()))
	t.Cleanup(ts.Close)

	status, body := apiCall(t, ts, http.MethodPost, "/v1/leases/lease-01/actions/rotate")
	if status != http.StatusOK {
		t.Fatalf("POST rotate = %d, want 200 (body %s)", status, body)
	}
	rotated := decode[model.Lease](t, body)
	if rotated.ID != "lease-01" || rotated.EgressIP != "203.0.113.99" ||
		rotated.ExpiresAt != "2026-10-23T13:00:00Z" || rotated.TTLSeconds != 28_800 {
		t.Fatalf("rotated lease JSON = %+v", rotated)
	}

	status, body = apiCall(t, ts, http.MethodDelete, "/v1/leases/lease-02")
	if status != http.StatusNoContent {
		t.Fatalf("DELETE lease = %d, want 204 (body %s)", status, body)
	}
	if len(body) != 0 {
		t.Fatalf("DELETE lease body = %q, want empty", body)
	}
	if _, ok := st.Lease("lease-02"); ok {
		t.Fatal("lease-02 still in the store after API release")
	}

	status, body = apiCall(t, ts, http.MethodPost, "/v1/nodes/node-06/actions/drain")
	if status != http.StatusOK {
		t.Fatalf("POST drain = %d, want 200 (body %s)", status, body)
	}
	drained := decode[model.ProxyNode](t, body)
	if drained.ID != "node-06" || drained.State != model.NodeDraining {
		t.Fatalf("drained node JSON = %+v", drained)
	}

	status, body = apiCall(t, ts, http.MethodPost, "/v1/nodes/node-06/actions/undrain")
	if status != http.StatusOK {
		t.Fatalf("POST undrain = %d, want 200 (body %s)", status, body)
	}
	undrained := decode[model.ProxyNode](t, body)
	if undrained.ID != "node-06" || undrained.State != model.NodeLive {
		t.Fatalf("undrained node JSON = %+v", undrained)
	}

	// Store conflicts still surface as 409 through the panel.
	status, body = apiCall(t, ts, http.MethodPost, "/v1/nodes/node-12/actions/drain")
	if status != http.StatusConflict {
		t.Fatalf("drain DOWN node = %d, want 409 (body %s)", status, body)
	}
	if code := errorCode(t, body); code != api.CodeConflict {
		t.Fatalf("drain DOWN code = %q, want %s", code, api.CodeConflict)
	}

	// Unknown ids are 404 through the panel.
	status, body = apiCall(t, ts, http.MethodPost, "/v1/leases/lease-99/actions/rotate")
	if status != http.StatusNotFound {
		t.Fatalf("rotate missing lease = %d, want 404 (body %s)", status, body)
	}
	if code := errorCode(t, body); code != api.CodeNotFound {
		t.Fatalf("missing lease code = %q, want %s", code, api.CodeNotFound)
	}
	status, body = apiCall(t, ts, http.MethodDelete, "/v1/leases/lease-99")
	if status != http.StatusNotFound {
		t.Fatalf("release missing lease = %d, want 404 (body %s)", status, body)
	}
	status, body = apiCall(t, ts, http.MethodPost, "/v1/nodes/node-99/actions/drain")
	if status != http.StatusNotFound {
		t.Fatalf("drain missing node = %d, want 404 (body %s)", status, body)
	}
}

func TestMutationFallbackWithoutPanel(t *testing.T) {
	st := store.New()
	ts := httptest.NewServer(api.New(api.Config{AllowInsecure: true}, st, discardLogger()))
	t.Cleanup(ts.Close)

	status, body := apiCall(t, ts, http.MethodPost, "/v1/leases/lease-01/actions/rotate")
	if status != http.StatusOK {
		t.Fatalf("fallback rotate = %d, want 200 (body %s)", status, body)
	}
	rotated := decode[model.Lease](t, body)
	if rotated.EgressIP != "203.0.113.24" || rotated.Requests != 18_449 {
		t.Fatalf("fallback rotate = %+v, want next pool IP and requests +17", rotated)
	}

	status, body = apiCall(t, ts, http.MethodDelete, "/v1/leases/lease-02")
	if status != http.StatusNoContent {
		t.Fatalf("fallback release = %d, want 204 (body %s)", status, body)
	}
	if _, ok := st.Lease("lease-02"); ok {
		t.Fatal("fallback release left the lease row")
	}

	status, body = apiCall(t, ts, http.MethodPost, "/v1/nodes/node-06/actions/drain")
	if status != http.StatusOK {
		t.Fatalf("fallback drain = %d, want 200 (body %s)", status, body)
	}
	if node := decode[model.ProxyNode](t, body); node.State != model.NodeDraining {
		t.Fatalf("fallback drain state = %q, want DRAINING", node.State)
	}
	status, body = apiCall(t, ts, http.MethodPost, "/v1/nodes/node-06/actions/undrain")
	if status != http.StatusOK {
		t.Fatalf("fallback undrain = %d, want 200 (body %s)", status, body)
	}
	if node := decode[model.ProxyNode](t, body); node.State != model.NodeLive {
		t.Fatalf("fallback undrain state = %q, want LIVE", node.State)
	}
}
