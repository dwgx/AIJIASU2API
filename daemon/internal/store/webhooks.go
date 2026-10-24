package store

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// MaxWebhookEndpoints bounds the persisted receiver list. The dispatcher's
// per-endpoint queues are bounded too, so an unbounded registry would multiply
// the delivery memory a single panel can allocate.
const MaxWebhookEndpoints = 32

// WebhookEndpoints returns the persisted receivers in creation order. The
// signing secret is not part of the result; callers resolve it separately.
func (s *Store) WebhookEndpoints() []model.WebhookEndpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.WebhookEndpoint, len(s.webhookEndpoints))
	for i, endpoint := range s.webhookEndpoints {
		out[i] = cloneWebhookEndpoint(endpoint)
	}
	return out
}

// WebhookEndpoint returns one receiver by id.
func (s *Store) WebhookEndpoint(id string) (model.WebhookEndpoint, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.webhookIndexLocked(id)
	if idx < 0 {
		return model.WebhookEndpoint{}, false
	}
	return cloneWebhookEndpoint(s.webhookEndpoints[idx]), true
}

// CreateWebhookEndpoint validates the receiver metadata and assigns its id.
// The URL scheme, host and event vocabulary are validated by the dispatcher,
// which owns the delivery contract; the store only rejects an unusable row.
func (s *Store) CreateWebhookEndpoint(in model.WebhookEndpointInput) (model.WebhookEndpoint, error) {
	url := strings.TrimSpace(in.URL)
	if url == "" {
		return model.WebhookEndpoint{}, invalidf("webhook url is required")
	}
	events := make([]string, 0, len(in.Events))
	seen := make(map[string]struct{}, len(in.Events))
	for _, event := range in.Events {
		event = strings.TrimSpace(event)
		if event == "" {
			continue
		}
		if _, ok := seen[event]; ok {
			continue
		}
		seen[event] = struct{}{}
		events = append(events, event)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.webhookEndpoints) >= MaxWebhookEndpoints {
		return model.WebhookEndpoint{}, invalidf("at most %d webhook endpoints are allowed", MaxWebhookEndpoints)
	}
	s.webhookSeq++
	endpoint := model.WebhookEndpoint{
		ID:          fmt.Sprintf("wh-%03d", s.webhookSeq),
		URL:         url,
		Events:      events,
		Enabled:     enabled,
		Description: strings.TrimSpace(in.Description),
		CreatedAt:   s.now().UTC().Format(time.RFC3339),
	}
	s.webhookEndpoints = append(s.webhookEndpoints, endpoint)
	return cloneWebhookEndpoint(endpoint), nil
}

// SetWebhookEndpointEnabled toggles one receiver. The dispatcher mirror is
// updated by the caller; this only owns the persisted row.
func (s *Store) SetWebhookEndpointEnabled(id string, enabled bool) (model.WebhookEndpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.webhookIndexLocked(id)
	if idx < 0 {
		return model.WebhookEndpoint{}, notFoundf("webhook %q not found", id)
	}
	s.webhookEndpoints[idx].Enabled = enabled
	return cloneWebhookEndpoint(s.webhookEndpoints[idx]), nil
}

// DeleteWebhookEndpoint removes one receiver. Deleting an unknown id reports
// not-found so DELETE can answer 404 instead of pretending the row existed.
// The receiver's retained deliveries go with it: the deliveries route 404s
// once the row is gone, and an attempt still queued in the dispatcher re-adds
// itself under the normal caps.
func (s *Store) DeleteWebhookEndpoint(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.webhookIndexLocked(id)
	if idx < 0 {
		return notFoundf("webhook %q not found", id)
	}
	s.webhookEndpoints = append(s.webhookEndpoints[:idx], s.webhookEndpoints[idx+1:]...)
	s.webhookDeliveries = slices.DeleteFunc(s.webhookDeliveries, func(delivery model.WebhookDelivery) bool {
		return delivery.EndpointID == id
	})
	return nil
}

func (s *Store) webhookIndexLocked(id string) int {
	for i, endpoint := range s.webhookEndpoints {
		if endpoint.ID == id {
			return i
		}
	}
	return -1
}

func cloneWebhookEndpoint(endpoint model.WebhookEndpoint) model.WebhookEndpoint {
	endpoint.Events = append([]string(nil), endpoint.Events...)
	return endpoint
}
