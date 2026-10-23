// Package lablog keeps a bounded, redacted in-memory copy of the daemon's
// structured logs so the read-only Lab page can show recent failures without
// a log shipper or a file on disk.
//
// The ring is deliberately strict: only an allowlist of attributes is stored
// (account_id, node_id, server_id, error_class, duration_ms, status, bytes)
// and no arbitrary slog attribute, message argument or error value reaches
// the buffer. A handler can tee to the normal stderr/stdout handler, so the
// ring never changes where logs go.
package lablog

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// DefaultCapacity is the number of entries the daemon retains.
const DefaultCapacity = 2000

// Entry is one redacted log line. Only allowlisted fields are ever populated.
type Entry struct {
	Time       time.Time
	Level      slog.Level
	Component  string
	Message    string
	AccountID  string
	NodeID     string
	ServerID   string
	ErrorClass string
	DurationMS *int64
	Status     *int
	Bytes      *int64
}

// Query selects a window of ring entries. Level is a minimum (the slog zero
// value is info; pass slog.LevelDebug to include debug lines). Limit bounds the
// most recent matches.
type Query struct {
	Since     time.Time
	Level     slog.Level
	Component string
	Limit     int
}

// Ring is a fixed-capacity FIFO of entries. It is safe for concurrent use.
type Ring struct {
	mu       sync.Mutex
	buf      []Entry
	start    int // index of the oldest entry once the ring is full
	count    int
	dropped  uint64
	capacity int
}

// NewRing returns a ring retaining capacity entries. A non-positive capacity
// selects DefaultCapacity.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Ring{
		buf:      make([]Entry, capacity),
		capacity: capacity,
	}
}

// Handler returns a slog.Handler that records every record in the ring and
// forwards it to next. A nil next keeps the ring recording and discards the
// console copy, which is useful in tests.
func (r *Ring) Handler(next slog.Handler) slog.Handler {
	return &handler{ring: r, next: next}
}

// Len returns the number of retained entries.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// Capacity returns the fixed buffer size.
func (r *Ring) Capacity() int { return r.capacity }

// Entries returns the matching entries in chronological order, at most limit
// of the most recent matches, plus the number of entries evicted by the ring
// since process start. A zero limit means every match; a limit above capacity
// is clamped to capacity.
func (r *Ring) Entries(since time.Time, minLevel slog.Level, component string, limit int) ([]Entry, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if limit <= 0 || limit > r.capacity {
		limit = r.capacity
	}
	out := make([]Entry, 0, limit)
	for i := range r.count {
		entry := r.buf[(r.start+i)%r.capacity]
		if !since.IsZero() && entry.Time.Before(since) {
			continue
		}
		if entry.Level < minLevel {
			continue
		}
		if component != "" && !strings.EqualFold(entry.Component, component) {
			continue
		}
		out = append(out, entry)
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, r.dropped
}

func (r *Ring) add(entry Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count < r.capacity {
		r.buf[(r.start+r.count)%r.capacity] = entry
		r.count++
		return
	}
	r.buf[r.start] = entry
	r.start = (r.start + 1) % r.capacity
	r.dropped++
}

// handler tees records into the ring and the wrapped handler.
type handler struct {
	ring   *Ring
	next   slog.Handler
	attrs  []slog.Attr
	groups []string
}

// Enabled always reports true for the ring: the console handler still applies
// its own level filter in Handle, so AJS_LOG_LEVEL=warn keeps the console
// quiet while the Lab ring retains debug/info lines for on-demand inspection.
func (h *handler) Enabled(context.Context, slog.Level) bool { return true }

func (h *handler) Handle(ctx context.Context, rec slog.Record) error {
	h.record(rec)
	if h.next != nil && h.next.Enabled(ctx, rec.Level) {
		return h.next.Handle(ctx, rec)
	}
	return nil
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	if h.next != nil {
		clone.next = h.next.WithAttrs(attrs)
	}
	return &clone
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = append(append([]string(nil), h.groups...), name)
	if h.next != nil {
		clone.next = h.next.WithGroup(name)
	}
	return &clone
}

func (h *handler) record(rec slog.Record) {
	if h.ring == nil {
		return
	}
	entry := Entry{
		Time:      rec.Time,
		Level:     rec.Level,
		Component: componentOf(rec.Message),
		Message:   rec.Message,
	}
	for _, attr := range h.attrs {
		applyAttr(&entry, attr)
	}
	rec.Attrs(func(attr slog.Attr) bool {
		applyAttr(&entry, attr)
		return true
	})
	h.ring.add(entry)
}

// componentOf derives a stable component from the message convention the
// daemon already uses: the first token before a space or underscore
// ("gate listening" -> "gate", "account_login" -> "account").
func componentOf(msg string) string {
	for i := range len(msg) {
		if msg[i] == ' ' || msg[i] == '_' {
			return msg[:i]
		}
	}
	return msg
}

// maxAttrLen bounds one identifier copied into the ring, so a pathological
// attribute cannot grow a retained entry without limit.
const maxAttrLen = 128

// applyAttr copies only allowlisted keys. The allowlist is intentionally
// narrower than the daemon's logging vocabulary: targets, error text, paths,
// method names and every credential-adjacent key stay out of the ring.
func applyAttr(entry *Entry, attr slog.Attr) {
	value := attr.Value.Resolve()
	if value.Kind() == slog.KindGroup {
		for _, nested := range value.Group() {
			applyAttr(entry, nested)
		}
		return
	}
	switch strings.ToLower(attr.Key) {
	case "account_id", "account":
		if text := stringValue(value); text != "" {
			entry.AccountID = text
		}
	case "node_id", "node":
		if text := stringValue(value); text != "" {
			entry.NodeID = text
		}
	case "server_id":
		if text := stringValue(value); text != "" {
			entry.ServerID = text
		}
	case "error_class":
		if text := stringValue(value); text != "" {
			entry.ErrorClass = text
		}
	case "duration_ms":
		if ms, ok := intValue(value); ok {
			entry.DurationMS = &ms
		}
	case "duration":
		// The gate logs a time.Duration; convert it to the allowlisted
		// duration_ms field rather than storing the raw value.
		if d, ok := value.Any().(time.Duration); ok {
			ms := d.Milliseconds()
			entry.DurationMS = &ms
		}
	case "status":
		if n, ok := intValue(value); ok && n >= 0 && n <= 999 {
			status := int(n)
			entry.Status = &status
		}
	case "bytes":
		if n, ok := intValue(value); ok {
			entry.Bytes = &n
		}
	}
}

func stringValue(value slog.Value) string {
	if value.Kind() != slog.KindString {
		return ""
	}
	text := value.String()
	if len(text) > maxAttrLen {
		text = text[:maxAttrLen]
	}
	return text
}

func intValue(value slog.Value) (int64, bool) {
	switch value.Kind() {
	case slog.KindInt64:
		return value.Int64(), true
	case slog.KindUint64:
		n := value.Uint64()
		if n > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(n), true
	case slog.KindFloat64:
		return int64(value.Float64()), true
	case slog.KindDuration:
		return value.Duration().Milliseconds(), true
	default:
		return 0, false
	}
}
