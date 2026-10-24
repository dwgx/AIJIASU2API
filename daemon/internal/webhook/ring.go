package webhook

import "sync"

// DefaultRingSize is the number of delivery attempts retained per endpoint and
// globally.
const DefaultRingSize = 200

// DeliveryLog is a thread-safe ring of the most recent delivery attempts. It
// keeps two independent rings: one global and one per endpoint. Both drop the
// oldest entry when full.
type DeliveryLog struct {
	mu       sync.Mutex
	capacity int
	global   []Delivery
	per      map[string][]Delivery
	retries  int
}

// NewDeliveryLog returns a log retaining capacity attempts per ring. A
// non-positive capacity selects DefaultRingSize.
func NewDeliveryLog(capacity int) *DeliveryLog {
	if capacity <= 0 {
		capacity = DefaultRingSize
	}
	return &DeliveryLog{capacity: capacity, per: make(map[string][]Delivery)}
}

// Append records one attempt.
func (l *DeliveryLog) Append(delivery Delivery) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.global = appendRing(l.global, delivery, l.capacity)
	l.per[delivery.EndpointID] = appendRing(l.per[delivery.EndpointID], delivery, l.capacity)
	if delivery.Attempt > 1 {
		l.retries++
	}
}

func appendRing(ring []Delivery, delivery Delivery, capacity int) []Delivery {
	if len(ring) == capacity {
		copy(ring, ring[1:])
		ring[len(ring)-1] = delivery
		return ring
	}
	return append(ring, delivery)
}

// Recent returns the global ring, oldest first.
func (l *DeliveryLog) Recent() []Delivery {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Delivery(nil), l.global...)
}

// RecentFor returns the ring for endpointID, oldest first.
func (l *DeliveryLog) RecentFor(endpointID string) []Delivery {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Delivery(nil), l.per[endpointID]...)
}

// Retries reports how many recorded attempts were retries (attempt > 1). The
// counter is not capped by the ring, so it measures lifetime retry pressure.
func (l *DeliveryLog) Retries() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.retries
}

// Len reports the number of attempts currently in the global ring.
func (l *DeliveryLog) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.global)
}
