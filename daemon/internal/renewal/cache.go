package renewal

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTTL is how long a live snapshot stays fresh.
	DefaultTTL = 6 * time.Hour
	// EnvTTL overrides DefaultTTL. It accepts any time.ParseDuration value;
	// an unparsable or non-positive value keeps the default.
	EnvTTL = "AJS_RENEWAL_TTL"
)

// Service is the cached catalogue: one live snapshot, refreshed on demand with
// single-flight semantics, plus a last-good and a static fallback. It is safe
// for concurrent use.
type Service struct {
	fetcher *Fetcher
	ttl     time.Duration
	now     func() time.Time

	mu       sync.Mutex
	has      bool
	cached   PlansSnapshot
	inflight *refreshCall
}

// refreshCall is one in-flight fetch shared by every concurrent caller.
type refreshCall struct {
	done     chan struct{}
	snapshot PlansSnapshot
	err      error
}

// New builds a Service from cfg. It performs no I/O.
func New(cfg Config) *Service {
	cfg = normalizeConfig(cfg)
	return &Service{
		fetcher: newFetcher(cfg),
		ttl:     cfg.TTL,
		now:     cfg.Now,
	}
}

// Plans returns the plan catalogue.
//
// A fresh cache is served without touching the network and reports
// SourceCache. Otherwise one caller fetches while every other caller waits for
// that same fetch. A failed fetch is soft: the last good snapshot is served
// with Stale true, or the built-in table with SourceStatic when nothing has
// ever been fetched. The returned error reports why the snapshot is not live;
// the snapshot itself is always safe to serve.
//
// The only case without a usable snapshot is the caller's context ending while
// it waits for somebody else's refresh.
func (s *Service) Plans(ctx context.Context) (PlansSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.has {
		age := s.now().Sub(s.cached.FetchedAt)
		if age >= 0 && age < s.ttl {
			snapshot := cloneSnapshot(s.cached)
			snapshot.Source = SourceCache
			s.mu.Unlock()
			return snapshot, nil
		}
	}
	call := s.inflight
	if call == nil {
		call = &refreshCall{done: make(chan struct{})}
		s.inflight = call
		s.mu.Unlock()
		s.refresh(ctx, call)
		return cloneSnapshot(call.snapshot), call.err
	}
	s.mu.Unlock()

	select {
	case <-call.done:
		return cloneSnapshot(call.snapshot), call.err
	case <-ctx.Done():
		return PlansSnapshot{}, ctx.Err()
	}
}

// Status derives one account's renewal state using the service clock.
func (s *Service) Status(accountID, expiresAt string) (AccountRenewal, error) {
	return Expiry(accountID, expiresAt, s.now())
}

// refresh performs the fetch and publishes its result. It is called by exactly
// one caller per refreshCall; waiters are released by closing done.
func (s *Service) refresh(ctx context.Context, call *refreshCall) {
	plans, err := s.fetcher.Fetch(ctx)
	if err == nil && len(plans) == 0 {
		err = errors.New("renewal: purchase page returned no plans")
	}

	s.mu.Lock()
	switch {
	case err == nil:
		s.cached = PlansSnapshot{
			Plans:     plans,
			FetchedAt: s.now().UTC(),
			Source:    SourceLive,
		}
		s.has = true
		call.snapshot = cloneSnapshot(s.cached)
	case s.has:
		call.snapshot = cloneSnapshot(s.cached)
		call.snapshot.Source = SourceCache
		call.snapshot.Stale = true
	default:
		call.snapshot = staticSnapshot()
	}
	call.err = err
	s.inflight = nil
	close(call.done)
	s.mu.Unlock()
}

// StaticPlans returns a copy of the built-in fallback catalogue. The table is
// the public list prices observed on 2026-10-24; it exists so a cold cache can
// still render something when the vendor page is unreachable or reshaped.
func StaticPlans() []Plan {
	return []Plan{
		{ID: "M1", Label: "1 month", Months: 1, PriceCNY: 20},
		{ID: "M3", Label: "3 months", Months: 3, PriceCNY: 53},
		{ID: "M12", Label: "12 months", Months: 12, PriceCNY: 198},
		{ID: "H6", Label: "6-hour trial", Months: 0, PriceCNY: 1, Trial: true, Notes: "TrialPackage"},
	}
}

func staticSnapshot() PlansSnapshot {
	return PlansSnapshot{
		Plans:  StaticPlans(),
		Source: SourceStatic,
		Stale:  true,
	}
}

// ttlFromEnv reads AJS_RENEWAL_TTL. A bad value is not fatal: the package
// prefers a working default over failing the panel over a typo.
func ttlFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv(EnvTTL))
	if raw == "" {
		return DefaultTTL
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil || ttl <= 0 {
		return DefaultTTL
	}
	return ttl
}

// clonePlans deep-copies a catalogue, including the auto-renew pointers, so a
// caller can never mutate the cache.
func clonePlans(plans []Plan) []Plan {
	if plans == nil {
		return nil
	}
	out := make([]Plan, len(plans))
	copy(out, plans)
	for i := range out {
		if out[i].AutoRenewPriceCNY != nil {
			price := *out[i].AutoRenewPriceCNY
			out[i].AutoRenewPriceCNY = &price
		}
	}
	return out
}

func cloneSnapshot(snapshot PlansSnapshot) PlansSnapshot {
	snapshot.Plans = clonePlans(snapshot.Plans)
	return snapshot
}
