// Package webhook delivers outbound events to customer integrations.
//
// A Dispatcher owns a bounded queue, a fixed worker pool, per-endpoint FIFO
// delivery, HMAC-SHA256 signing, exponential backoff and a dead-letter state.
// Every delivery attempt is recorded in a ring buffer of the last 200 attempts,
// per endpoint and globally, so operators can inspect failures without a
// database.
//
// The package never logs payloads, signatures or endpoint secrets: log lines
// carry ids, event types and HTTP status only. Payload data is scrubbed before
// it leaves the process, so credentials a caller accidentally puts in an event
// map never reach the wire.
package webhook

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// EventType names a customer-visible event.
type EventType string

// The event catalogue. The strings are the wire values in X-APC-Event and in
// the payload, so they must not change without a customer-facing version bump.
const (
	EventAccountLogin      EventType = "account.login"
	EventAccountLogout     EventType = "account.logout"
	EventAccountExpired    EventType = "account.expired"
	EventAccountRenewalDue EventType = "account.renewal_due"
	EventNodeConnected     EventType = "node.connected"
	EventNodeDisconnected  EventType = "node.disconnected"
	EventNodeSwitched      EventType = "node.switched"
	EventScheduleFired     EventType = "schedule.fired"
	EventScheduleFailed    EventType = "schedule.failed"
	EventDeviceRetired     EventType = "device.retired"
	EventAuditCritical     EventType = "audit.critical"
)

// Valid reports whether e is one of the defined event types.
func (e EventType) Valid() bool {
	switch e {
	case EventAccountLogin,
		EventAccountLogout,
		EventAccountExpired,
		EventAccountRenewalDue,
		EventNodeConnected,
		EventNodeDisconnected,
		EventNodeSwitched,
		EventScheduleFired,
		EventScheduleFailed,
		EventDeviceRetired,
		EventAuditCritical:
		return true
	}
	return false
}

// DeliveryStatus is the outcome of one delivery attempt.
type DeliveryStatus string

const (
	// StatusSuccess means the endpoint answered 2xx.
	StatusSuccess DeliveryStatus = "success"
	// StatusFailed means the attempt failed but the dispatcher may try again
	// (or the failure was permanent).
	StatusFailed DeliveryStatus = "failed"
	// StatusDeadLetter means the final allowed attempt failed; the dispatcher
	// stops retrying this delivery.
	StatusDeadLetter DeliveryStatus = "dead_letter"
)

// Endpoint is one customer receiver. An empty Events list subscribes to every
// event type.
type Endpoint struct {
	ID          string      `json:"id"`
	URL         string      `json:"url"`
	Secret      string      `json:"secret"`
	Events      []EventType `json:"events,omitempty"`
	Enabled     bool        `json:"enabled"`
	CreatedAt   time.Time   `json:"createdAt"`
	Description string      `json:"description,omitempty"`
}

// subscribes reports whether ep wants event.
func (e Endpoint) subscribes(event EventType) bool {
	if len(e.Events) == 0 {
		return true
	}
	for _, candidate := range e.Events {
		if candidate == event {
			return true
		}
	}
	return false
}

// Event is one outbound event supplied by a caller. ID is the dedupe key: the
// same ID is accepted at most once per dedupe window, so a retrying producer
// cannot fan out duplicates. At defaults to the dispatcher clock.
type Event struct {
	ID         string
	EndpointID string
	Type       EventType
	Data       map[string]any
	At         time.Time
}

// Payload is the JSON body posted to an endpoint.
type Payload struct {
	ID    string         `json:"id"`
	Event EventType      `json:"event"`
	At    time.Time      `json:"at"`
	Data  map[string]any `json:"data"`
}

// Delivery records one attempt. The dispatcher keeps the last 200 attempts.
type Delivery struct {
	ID         string         `json:"id"`
	EndpointID string         `json:"endpointId"`
	Event      EventType      `json:"event"`
	Attempt    int            `json:"attempt"`
	Status     DeliveryStatus `json:"status"`
	HTTPStatus int            `json:"httpStatus,omitempty"`
	Error      string         `json:"error,omitempty"`
	At         time.Time      `json:"at"`
	DurationMs int64          `json:"durationMs"`
}

// Errors are matched with errors.Is. No error carries a secret or payload.
var (
	ErrQueueFull        = errors.New("webhook: queue is full")
	ErrClosed           = errors.New("webhook: dispatcher is closed")
	ErrDuplicate        = errors.New("webhook: duplicate event id within the dedupe window")
	ErrUnknownEndpoint  = errors.New("webhook: unknown endpoint")
	ErrEndpointExists   = errors.New("webhook: endpoint already exists")
	ErrEndpointMissing  = errors.New("webhook: endpoint not found")
	ErrEndpointDisabled = errors.New("webhook: endpoint is disabled")
	ErrNotSubscribed    = errors.New("webhook: endpoint does not subscribe to the event")
	ErrInvalidEndpoint  = errors.New("webhook: invalid endpoint")
	ErrInvalidEvent     = errors.New("webhook: invalid event")
)

// validateEndpoint checks everything that must hold before an endpoint can
// receive traffic. HTTP is only acceptable for loopback receivers and for
// deployments that opt in through AJS_WEBHOOK_ALLOW_HTTP.
func validateEndpoint(ep Endpoint, allowHTTP bool) error {
	if ep.ID == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidEndpoint)
	}
	if ep.Secret == "" {
		return fmt.Errorf("%w: secret is required", ErrInvalidEndpoint)
	}
	parsed, err := url.Parse(ep.URL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEndpoint, err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("%w: scheme must be https", ErrInvalidEndpoint)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%w: host is required", ErrInvalidEndpoint)
	}
	if parsed.User != nil {
		return fmt.Errorf("%w: credentials must not be embedded in the URL", ErrInvalidEndpoint)
	}
	if parsed.Scheme == "http" && !allowHTTP && !isLoopbackHost(parsed.Hostname()) {
		return fmt.Errorf("%w: http is only allowed for loopback receivers", ErrInvalidEndpoint)
	}
	for _, event := range ep.Events {
		if !event.Valid() {
			return fmt.Errorf("%w: unknown event type %q", ErrInvalidEndpoint, event)
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
