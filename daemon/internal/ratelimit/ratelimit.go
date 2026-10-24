// Package ratelimit implements the per-scoped-key token bucket enforced by the
// API auth middleware. A bucket holds one minute's allowance and refills
// lazily on access, so an idle key costs no goroutine and no timer.
package ratelimit

import (
	"sync"
	"time"
)

// DefaultIdleTTL is how long a bucket may stay untouched before Allow reaps
// it. Reaping only discards state: a key that returns after the TTL starts
// with a full bucket, exactly as a brand-new key does.
const DefaultIdleTTL = 10 * time.Minute

// sweepInterval bounds how often Allow scans the bucket map for idle entries.
// The scan is O(buckets) and only runs after this much time has passed, so the
// common path stays O(1).
const sweepInterval = time.Minute

// bucket is one key's token balance. tokens is fractional because a request
// may arrive mid-refill; remaining is always the whole tokens left.
type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter is a concurrency-safe map of key id to token bucket.
type Limiter struct {
	now     func() time.Time
	idleTTL time.Duration

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

// Option configures a Limiter.
type Option func(*Limiter)

// WithClock replaces the wall clock. Tests use it to prove refill and garbage
// collection without sleeping; nil keeps time.Now.
func WithClock(now func() time.Time) Option {
	return func(l *Limiter) {
		if now != nil {
			l.now = now
		}
	}
}

// WithIdleTTL overrides DefaultIdleTTL. It exists for tests that want to
// observe garbage collection without advancing the clock by ten minutes.
func WithIdleTTL(ttl time.Duration) Option {
	return func(l *Limiter) {
		if ttl > 0 {
			l.idleTTL = ttl
		}
	}
}

// New builds an empty Limiter.
func New(opts ...Option) *Limiter {
	l := &Limiter{
		now:     time.Now,
		idleTTL: DefaultIdleTTL,
		buckets: make(map[string]*bucket),
	}
	for _, opt := range opts {
		opt(l)
	}
	l.lastSweep = l.now()
	return l
}

// Allow consumes one token for keyID under limit requests per minute.
//
// limit <= 0 means unlimited: the call always succeeds, remaining is 0, reset
// is the zero time and no bucket is retained. remaining is the whole tokens
// left after this call. reset is when the bucket refills to full capacity, so
// a caller can render X-RateLimit-Reset; a denied caller should retry after
// one refill interval (60/limit seconds), not the full reset.
//
// Buckets refill lazily at limit/60 tokens per second, so a key that was idle
// for a long time is full on its next call. Buckets untouched for idleTTL are
// swept during Allow.
func (l *Limiter) Allow(keyID string, limit int) (ok bool, remaining int, reset time.Time) {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweepLocked(now)

	if limit <= 0 {
		// A key whose limit was removed must not keep stale state.
		delete(l.buckets, keyID)
		return true, 0, time.Time{}
	}

	capacity := float64(limit)
	rate := capacity / 60 // tokens per second

	b, found := l.buckets[keyID]
	if !found {
		b = &bucket{tokens: capacity, last: now}
		l.buckets[keyID] = b
	} else {
		elapsed := now.Sub(b.last)
		if elapsed < 0 {
			// A clock that moved backwards must not mint tokens.
			elapsed = 0
		}
		b.tokens += elapsed.Seconds() * rate
		if b.tokens > capacity {
			b.tokens = capacity
		}
		b.last = now
	}

	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	remaining = int(b.tokens)
	if missing := capacity - b.tokens; missing > 0 {
		reset = now.Add(time.Duration(missing / rate * float64(time.Second)))
	} else {
		reset = now
	}
	return allowed, remaining, reset
}

// sweepLocked drops buckets that have been idle for at least idleTTL. It runs
// at most once per sweepInterval so a burst of requests does not rescan the
// map.
func (l *Limiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < sweepInterval {
		return
	}
	l.lastSweep = now
	for id, b := range l.buckets {
		if now.Sub(b.last) >= l.idleTTL {
			delete(l.buckets, id)
		}
	}
}
