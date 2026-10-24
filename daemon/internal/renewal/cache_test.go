package renewal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func pageServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func TestPlansLiveThenCacheThenExpiry(t *testing.T) {
	var requests atomic.Int64
	server := pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, livePageFixture)
	})
	clock := newFakeClock(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	service := New(Config{BaseURL: server.URL, TTL: 6 * time.Hour, Now: clock.Now})

	first, err := service.Plans(context.Background())
	if err != nil {
		t.Fatalf("first Plans: %v", err)
	}
	if first.Source != SourceLive || first.Stale {
		t.Fatalf("first snapshot = %+v, want live and fresh", first)
	}
	if !first.FetchedAt.Equal(clock.Now()) {
		t.Fatalf("FetchedAt = %s, want %s", first.FetchedAt, clock.Now())
	}
	if len(first.Plans) != 4 {
		t.Fatalf("len(plans) = %d, want 4", len(first.Plans))
	}

	second, err := service.Plans(context.Background())
	if err != nil {
		t.Fatalf("second Plans: %v", err)
	}
	if second.Source != SourceCache || second.Stale {
		t.Fatalf("second snapshot = %+v, want a fresh cache hit", second)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 for a cache hit", got)
	}

	clock.Advance(6*time.Hour - time.Nanosecond)
	if _, err := service.Plans(context.Background()); err != nil {
		t.Fatalf("Plans just inside TTL: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 just inside TTL", got)
	}

	clock.Advance(time.Nanosecond)
	refreshed, err := service.Plans(context.Background())
	if err != nil {
		t.Fatalf("Plans at TTL: %v", err)
	}
	if refreshed.Source != SourceLive {
		t.Fatalf("source = %q, want live after TTL", refreshed.Source)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 after TTL", got)
	}
}

func TestPlansServesLastGoodWhenRefreshFails(t *testing.T) {
	var requests atomic.Int64
	var fail atomic.Bool
	server := pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, livePageFixture)
	})
	clock := newFakeClock(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	service := New(Config{BaseURL: server.URL, TTL: time.Hour, Now: clock.Now})

	live, err := service.Plans(context.Background())
	if err != nil {
		t.Fatalf("first Plans: %v", err)
	}

	clock.Advance(2 * time.Hour)
	fail.Store(true)
	fallback, err := service.Plans(context.Background())
	if err == nil {
		t.Fatal("Plans hid the refresh failure")
	}
	if fallback.Source != SourceCache || !fallback.Stale {
		t.Fatalf("fallback = %+v, want stale cache", fallback)
	}
	if !reflect.DeepEqual(fallback.Plans, live.Plans) {
		t.Fatalf("fallback plans = %+v, want %+v", fallback.Plans, live.Plans)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}

	fail.Store(false)
	recovered, err := service.Plans(context.Background())
	if err != nil {
		t.Fatalf("recovery Plans: %v", err)
	}
	if recovered.Source != SourceLive || recovered.Stale {
		t.Fatalf("recovered = %+v, want live", recovered)
	}
}

func TestPlansServesLastGoodWhenPageReshaped(t *testing.T) {
	var reshaped atomic.Bool
	server := pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		if reshaped.Load() {
			_, _ = io.WriteString(w, "<html><body>new frontend</body></html>")
			return
		}
		_, _ = io.WriteString(w, livePageFixture)
	})
	clock := newFakeClock(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	service := New(Config{BaseURL: server.URL, TTL: time.Hour, Now: clock.Now})

	live, err := service.Plans(context.Background())
	if err != nil {
		t.Fatalf("first Plans: %v", err)
	}
	clock.Advance(2 * time.Hour)
	reshaped.Store(true)
	fallback, err := service.Plans(context.Background())
	if err == nil {
		t.Fatal("Plans hid the parse failure")
	}
	if fallback.Source != SourceCache || !fallback.Stale {
		t.Fatalf("fallback = %+v, want stale cache", fallback)
	}
	if !reflect.DeepEqual(fallback.Plans, live.Plans) {
		t.Fatalf("fallback plans = %+v, want the last good snapshot", fallback.Plans)
	}
}

func TestPlansStaticFallbackOnNetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, livePageFixture)
	}))
	server.Close()

	service := New(Config{BaseURL: server.URL})
	snapshot, err := service.Plans(context.Background())
	if err == nil {
		t.Fatal("Plans hid the network error")
	}
	for _, forbidden := range []string{"_client_login", "token", "cookie", "authorization"} {
		if strings.Contains(strings.ToLower(err.Error()), forbidden) {
			t.Fatalf("fallback error leaks %q: %s", forbidden, err)
		}
	}
	if snapshot.Source != SourceStatic || !snapshot.Stale {
		t.Fatalf("snapshot = %+v, want static and stale", snapshot)
	}
	if !snapshot.FetchedAt.IsZero() {
		t.Fatalf("FetchedAt = %s, want zero for static", snapshot.FetchedAt)
	}
	if !reflect.DeepEqual(snapshot.Plans, StaticPlans()) {
		t.Fatalf("plans = %+v, want the static table", snapshot.Plans)
	}
}

func TestPlansStaticFallbackOnMalformedPage(t *testing.T) {
	server := pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<html><body>the page was redesigned</body></html>")
	})
	service := New(Config{BaseURL: server.URL})
	snapshot, err := service.Plans(context.Background())
	if err == nil {
		t.Fatal("Plans hid the parse failure")
	}
	if snapshot.Source != SourceStatic || !snapshot.Stale {
		t.Fatalf("snapshot = %+v, want static and stale", snapshot)
	}
}

func TestPlansSingleFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var requests atomic.Int64
	server := pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		once.Do(func() { close(started) })
		<-release
		_, _ = io.WriteString(w, livePageFixture)
	})
	service := New(Config{BaseURL: server.URL, TTL: time.Hour})

	const callers = 8
	var wg sync.WaitGroup
	results := make([]PlansSnapshot, callers)
	errs := make([]error, callers)
	ready := make(chan struct{})
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-ready
			results[i], errs[i] = service.Plans(context.Background())
		}(i)
	}
	close(ready)
	<-started

	// The handler is blocked, so any duplicate fetch would show up now.
	time.Sleep(50 * time.Millisecond)
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 while the first fetch is in flight", got)
	}

	close(release)
	wg.Wait()
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if len(results[i].Plans) != 4 {
			t.Fatalf("caller %d: len(plans) = %d, want 4", i, len(results[i].Plans))
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestPlansWaitingCallerCanCancel(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, livePageFixture)
	})
	service := New(Config{BaseURL: server.URL})

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = service.Plans(context.Background())
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Plans(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Plans error = %v, want context.Canceled", err)
	}
	close(release)
	<-firstDone
}

func TestPlansReturnsIsolatedCopies(t *testing.T) {
	server := pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, livePageFixture)
	})
	service := New(Config{BaseURL: server.URL})

	first, err := service.Plans(context.Background())
	if err != nil {
		t.Fatalf("first Plans: %v", err)
	}
	first.Plans[0].PriceCNY = 999
	*first.Plans[0].AutoRenewPriceCNY = 999

	second, err := service.Plans(context.Background())
	if err != nil {
		t.Fatalf("second Plans: %v", err)
	}
	if second.Plans[0].PriceCNY != 20 {
		t.Fatalf("cached PriceCNY = %d, want 20", second.Plans[0].PriceCNY)
	}
	if second.Plans[0].AutoRenewPriceCNY == nil || *second.Plans[0].AutoRenewPriceCNY != 20 {
		t.Fatalf("cached auto-renew = %v, want 20", second.Plans[0].AutoRenewPriceCNY)
	}
}

func TestStaticPlans(t *testing.T) {
	plans := StaticPlans()
	want := []Plan{
		{ID: "M1", Label: "1 month", Months: 1, PriceCNY: 20},
		{ID: "M3", Label: "3 months", Months: 3, PriceCNY: 53},
		{ID: "M12", Label: "12 months", Months: 12, PriceCNY: 198},
		{ID: "H6", Label: "6-hour trial", Months: 0, PriceCNY: 1, Trial: true, Notes: "TrialPackage"},
	}
	if !reflect.DeepEqual(plans, want) {
		t.Fatalf("StaticPlans = %+v, want %+v", plans, want)
	}

	plans[0].PriceCNY = 1
	if again := StaticPlans(); again[0].PriceCNY != 20 {
		t.Fatalf("StaticPlans is shared: got %d, want 20", again[0].PriceCNY)
	}
}

func TestTTLFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", DefaultTTL},
		{"45m", 45 * time.Minute},
		{"6h", 6 * time.Hour},
		{"not-a-duration", DefaultTTL},
		{"-1h", DefaultTTL},
		{"0s", DefaultTTL},
	}
	for _, tc := range cases {
		t.Setenv(EnvTTL, tc.raw)
		if got := New(Config{}).ttl; got != tc.want {
			t.Fatalf("AJS_RENEWAL_TTL=%q: ttl = %s, want %s", tc.raw, got, tc.want)
		}
	}
}

func TestExplicitTTLOverridesEnv(t *testing.T) {
	t.Setenv(EnvTTL, "45m")
	if got := New(Config{TTL: time.Minute}).ttl; got != time.Minute {
		t.Fatalf("ttl = %s, want 1m", got)
	}
}
