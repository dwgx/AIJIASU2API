package store

import (
	"sort"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// Retention caps for the persisted delivery history. They are deliberately
// much smaller than the dispatcher's 200-attempt in-memory ring: a delivery
// row is an audit breadcrumb, not a log, and the snapshot is rewritten whole
// on every save. A new attempt past either cap drops the oldest row.
const (
	// WebhookDeliveriesPerEndpoint bounds the rows kept for one receiver.
	WebhookDeliveriesPerEndpoint = 50
	// WebhookDeliveryRetention bounds the rows kept across every receiver.
	WebhookDeliveryRetention = 500
)

// AppendWebhookDelivery records one delivery attempt. The row is prepended
// (newest first) and both retention caps are enforced by dropping the oldest
// rows. The append only touches memory: the periodic snapshot carries it to
// disk, so a delivery never waits on a write or an fsync.
//
// An unknown endpoint id is accepted on purpose: the dispatcher lets queued
// attempts finish after a receiver is deleted, and those attempts must still be
// recorded rather than lost.
func (s *Store) AppendWebhookDelivery(delivery model.WebhookDelivery) error {
	if err := validateWebhookDelivery(delivery); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A delivery id is event id + attempt, and a producer may reuse an event
	// id once the dispatcher's dedupe window has passed. Replace the old row
	// instead of accumulating: the API dedupes by id anyway, and duplicate
	// rows would make the snapshot ambiguous.
	for i, existing := range s.webhookDeliveries {
		if existing.ID == delivery.ID {
			s.webhookDeliveries = append(s.webhookDeliveries[:i], s.webhookDeliveries[i+1:]...)
			break
		}
	}
	s.webhookDeliveries = append(s.webhookDeliveries, model.WebhookDelivery{})
	copy(s.webhookDeliveries[1:], s.webhookDeliveries[:len(s.webhookDeliveries)-1])
	s.webhookDeliveries[0] = delivery
	if len(s.webhookDeliveries) > WebhookDeliveryRetention {
		s.webhookDeliveries = s.webhookDeliveries[:WebhookDeliveryRetention]
	}
	// Drop this endpoint's rows past its own cap. The slice is newest first,
	// so the first cap-many rows are kept and every later one is removed.
	kept := 0
	for i := 0; i < len(s.webhookDeliveries); {
		if s.webhookDeliveries[i].EndpointID != delivery.EndpointID {
			i++
			continue
		}
		kept++
		if kept > WebhookDeliveriesPerEndpoint {
			s.webhookDeliveries = append(s.webhookDeliveries[:i], s.webhookDeliveries[i+1:]...)
			continue
		}
		i++
	}
	return nil
}

// WebhookDeliveries returns the retained attempts for one endpoint, newest
// first. The result is a copy, so a caller may sort or truncate it without
// touching the store.
func (s *Store) WebhookDeliveries(endpointID string) []model.WebhookDelivery {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.WebhookDelivery, 0, min(len(s.webhookDeliveries), WebhookDeliveriesPerEndpoint))
	for _, delivery := range s.webhookDeliveries {
		if delivery.EndpointID == endpointID {
			out = append(out, delivery)
		}
	}
	return out
}

// validateWebhookDelivery rejects a row that could not be served or ordered.
// The event vocabulary and the status values stay the dispatcher's contract;
// the store only needs identity, an endpoint, a timestamp and non-negative
// counters.
func validateWebhookDelivery(delivery model.WebhookDelivery) error {
	if strings.TrimSpace(delivery.ID) == "" {
		return invalidf("webhook delivery has no id")
	}
	if strings.TrimSpace(delivery.EndpointID) == "" {
		return invalidf("webhook delivery %q has no endpoint", delivery.ID)
	}
	if strings.TrimSpace(delivery.Event) == "" {
		return invalidf("webhook delivery %q has no event", delivery.ID)
	}
	if strings.TrimSpace(delivery.Status) == "" {
		return invalidf("webhook delivery %q has no status", delivery.ID)
	}
	if delivery.Attempt < 0 || delivery.HTTPStatus < 0 || delivery.DurationMs < 0 {
		return invalidf("webhook delivery %q has a negative counter", delivery.ID)
	}
	if !validRFC3339(delivery.At) {
		return invalidf("webhook delivery %q has a non-RFC3339 timestamp", delivery.ID)
	}
	return nil
}

// normalizeWebhookDeliveries enforces the snapshot invariant: newest first, at
// most WebhookDeliveriesPerEndpoint rows per endpoint and
// WebhookDeliveryRetention rows overall. A document written by an older build
// or edited by hand is trimmed rather than rejected, the same way Restore
// treats the request and audit histories.
func normalizeWebhookDeliveries(deliveries []model.WebhookDelivery) []model.WebhookDelivery {
	if len(deliveries) == 0 {
		return []model.WebhookDelivery{}
	}
	sort.SliceStable(deliveries, func(i, j int) bool {
		// Validation already proved both timestamps parse; the zero time only
		// keeps the comparator total if that ever stops holding.
		left, _ := time.Parse(time.RFC3339, deliveries[i].At)
		right, _ := time.Parse(time.RFC3339, deliveries[j].At)
		return left.After(right)
	})
	kept := make([]model.WebhookDelivery, 0, min(len(deliveries), WebhookDeliveryRetention))
	perEndpoint := make(map[string]int, len(deliveries))
	seen := make(map[string]struct{}, len(deliveries))
	for _, delivery := range deliveries {
		if len(kept) >= WebhookDeliveryRetention {
			break
		}
		// A hand-edited document may repeat an id; the newest copy is the one
		// already visited by this newest-first walk.
		if _, ok := seen[delivery.ID]; ok {
			continue
		}
		seen[delivery.ID] = struct{}{}
		if perEndpoint[delivery.EndpointID] >= WebhookDeliveriesPerEndpoint {
			continue
		}
		perEndpoint[delivery.EndpointID]++
		kept = append(kept, delivery)
	}
	return kept
}

// validateWebhookDeliveries rejects a snapshot whose delivery rows cannot be
// served as-is. Over-cap documents are trimmed and duplicate ids collapsed by
// normalize instead of rejecting the whole panel.
func (snap *Snapshot) validateWebhookDeliveries() error {
	for _, delivery := range snap.WebhookDeliveries {
		if err := validateWebhookDelivery(delivery); err != nil {
			return err
		}
	}
	return nil
}
