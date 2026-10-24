package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestAllowConsumesTokensAndReportsReset(t *testing.T) {
	clock := newFakeClock()
	l := New(WithClock(clock.Now))

	ok, remaining, reset := l.Allow("sk-01", 60)
	if !ok {
		t.Fatal("first request was refused")
	}
	if remaining != 59 {
		t.Fatalf("remaining = %d, want 59", remaining)
	}
	if want := clock.Now().Add(time.Second); !reset.Equal(want) {
		t.Fatalf("reset = %v, want %v", reset, want)
	}

	for i := range 59 {
		if ok, _, _ = l.Allow("sk-01", 60); !ok {
			t.Fatalf("request %d was refused while tokens remained", i+2)
		}
	}

	ok, remaining, reset = l.Allow("sk-01", 60)
	if ok {
		t.Fatal("request succeeded after the bucket emptied")
	}
	if remaining != 0 {
		t.Fatalf("remaining = %d, want 0", remaining)
	}
	if want := clock.Now().Add(time.Minute); !reset.Equal(want) {
		t.Fatalf("denied reset = %v, want %v", reset, want)
	}
}

func TestRefillUsesInjectedClock(t *testing.T) {
	clock := newFakeClock()
	l := New(WithClock(clock.Now))

	for i := range 60 {
		if ok, _, _ := l.Allow("sk-01", 60); !ok {
			t.Fatalf("request %d refused on a full bucket", i+1)
		}
	}
	if ok, _, _ := l.Allow("sk-01", 60); ok {
		t.Fatal("request succeeded on an empty bucket")
	}

	clock.Advance(500 * time.Millisecond)
	if ok, _, _ := l.Allow("sk-01", 60); ok {
		t.Fatal("half a refill interval produced a token")
	}

	clock.Advance(500 * time.Millisecond)
	ok, remaining, _ := l.Allow("sk-01", 60)
	if !ok {
		t.Fatal("one refill interval did not produce a token")
	}
	if remaining != 0 {
		t.Fatalf("remaining = %d, want 0 after consuming the refilled token", remaining)
	}

	// A long idle period refills to capacity and never beyond it.
	clock.Advance(24 * time.Hour)
	ok, remaining, _ = l.Allow("sk-01", 60)
	if !ok || remaining != 59 {
		t.Fatalf("after a long idle period: ok=%v remaining=%d, want true/59", ok, remaining)
	}
}

func TestUnlimitedKeepsNoBucket(t *testing.T) {
	clock := newFakeClock()
	l := New(WithClock(clock.Now))

	for i := range 1000 {
		ok, remaining, reset := l.Allow("sk-01", 0)
		if !ok {
			t.Fatalf("unlimited request %d was refused", i+1)
		}
		if remaining != 0 || !reset.IsZero() {
			t.Fatalf("unlimited result = (%d, %v), want (0, zero)", remaining, reset)
		}
	}
	if got := len(l.buckets); got != 0 {
		t.Fatalf("unlimited key retained %d buckets", got)
	}

	// Dropping the limit also drops the state accumulated under the old one.
	if ok, _, _ := l.Allow("sk-02", 1); !ok {
		t.Fatal("limited request was refused")
	}
	if ok, _, _ := l.Allow("sk-02", -1); !ok {
		t.Fatal("negative limit was not treated as unlimited")
	}
	if _, found := l.buckets["sk-02"]; found {
		t.Fatal("unlimited key kept its bucket")
	}
}

func TestKeysAreIsolated(t *testing.T) {
	l := New(WithClock(newFakeClock().Now))

	if ok, _, _ := l.Allow("sk-01", 1); !ok {
		t.Fatal("first key's first request was refused")
	}
	if ok, _, _ := l.Allow("sk-01", 1); ok {
		t.Fatal("first key exceeded its own limit")
	}
	if ok, _, _ := l.Allow("sk-02", 1); !ok {
		t.Fatal("second key was affected by the first key's bucket")
	}
}

func TestIdleBucketsAreCollected(t *testing.T) {
	clock := newFakeClock()
	l := New(WithClock(clock.Now), WithIdleTTL(time.Minute))

	if ok, _, _ := l.Allow("sk-01", 60); !ok {
		t.Fatal("first request was refused")
	}
	clock.Advance(2 * time.Minute)

	// This call triggers the sweep; the idle bucket must not survive.
	if ok, remaining, _ := l.Allow("sk-01", 60); !ok || remaining != 59 {
		t.Fatalf("after GC: ok=%v remaining=%d, want true/59 from a fresh bucket", ok, remaining)
	}
	if got := len(l.buckets); got != 1 {
		t.Fatalf("buckets after GC = %d, want 1", got)
	}
}

func TestLimitChangeClampsToNewCapacity(t *testing.T) {
	clock := newFakeClock()
	l := New(WithClock(clock.Now))

	if ok, remaining, _ := l.Allow("sk-01", 10); !ok || remaining != 9 {
		t.Fatalf("first request = %v/%d, want true/9", ok, remaining)
	}
	// Lowering the limit clamps the balance on the next access instead of
	// letting the old allowance leak through.
	ok, remaining, _ := l.Allow("sk-01", 2)
	if !ok {
		t.Fatal("clamped request was refused")
	}
	if remaining != 1 {
		t.Fatalf("remaining = %d, want 1 (capacity 2, one consumed)", remaining)
	}
}

func TestAllowIsSafeForConcurrentCallers(t *testing.T) {
	l := New(WithClock(newFakeClock().Now))

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _, _ := l.Allow("sk-01", 10); ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := allowed.Load(); got != 10 {
		t.Fatalf("allowed = %d, want exactly 10", got)
	}
}
