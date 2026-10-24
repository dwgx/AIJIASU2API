package health

import (
	"math"
	"sort"
)

// ringBuffer is a fixed-capacity FIFO of samples. It is not goroutine-safe;
// the Sampler guards it with its mutex.
type ringBuffer struct {
	buf  []Sample
	next int // index of the slot after the newest sample
	size int
}

func newRing(capacity int) ringBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return ringBuffer{buf: make([]Sample, capacity)}
}

// append writes one sample, overwriting the oldest when full.
func (r *ringBuffer) append(sample Sample) {
	if len(r.buf) == 0 {
		return
	}
	r.buf[r.next] = sample
	r.next = (r.next + 1) % len(r.buf)
	if r.size < len(r.buf) {
		r.size++
	}
}

// samples copies the window oldest-first.
func (r *ringBuffer) samples() []Sample {
	out := make([]Sample, r.size)
	start := 0
	if r.size == len(r.buf) {
		start = r.next
	}
	for i := range r.size {
		out[i] = r.buf[(start+i)%len(r.buf)]
	}
	return out
}

// aggregate folds a window (oldest-first) into Stats. Percentiles use the
// nearest-rank method over successful samples: p50 of n samples is the
// ceil(0.50*n)-th smallest.
func aggregate(key string, samples []Sample) Stats {
	stats := Stats{Key: key, Count: len(samples), State: StateOK}
	if len(samples) == 0 {
		return stats
	}
	stats.Last = samples[len(samples)-1]

	latencies := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if sample.OK {
			latencies = append(latencies, sample.LatencyMs)
		}
	}
	stats.OK = len(latencies)
	stats.SuccessRate = float64(stats.OK) / float64(stats.Count)
	if len(latencies) > 0 {
		sort.Float64s(latencies)
		stats.MinMs = latencies[0]
		stats.MaxMs = latencies[len(latencies)-1]
		stats.P50Ms = percentile(latencies, 0.50)
		stats.P95Ms = percentile(latencies, 0.95)
	}
	for i := len(samples) - 1; i >= 0 && !samples[i].OK; i-- {
		stats.ConsecutiveFailures++
	}
	stats.State = healthState(stats)
	return stats
}

// percentile returns the nearest-rank percentile of a sorted, non-empty slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// healthState applies the console state machine. Down wins over degraded:
// three consecutive failures or a success rate under 50% is down; a p95 over
// 800ms or a success rate under 90% is degraded. An empty window is ok: there
// is no evidence of failure yet.
func healthState(stats Stats) string {
	if stats.Count == 0 {
		return StateOK
	}
	if stats.ConsecutiveFailures >= downAfterFailures || stats.SuccessRate < downBelowSuccess {
		return StateDown
	}
	if stats.P95Ms > degradedAboveP95Ms || stats.SuccessRate < degradedBelowSuccess {
		return StateDegraded
	}
	return StateOK
}
