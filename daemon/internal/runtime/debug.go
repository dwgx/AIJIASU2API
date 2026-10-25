// This file implements the real-time debugger trace. The debugger backend
// reuses the existing events.Ring as its storage and fan-out engine: the same
// bounded FIFO, monotonic sequence, subscriber semantics and eviction
// accounting as the customer event stream. It adds only the typed trace entry,
// the stricter debugger redaction and the runtime instrumentation that feeds
// it.

package runtime

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"ajsaccountd/internal/events"
	"ajsaccountd/internal/model"
)

// DebugRingCapacity is the fixed number of trace entries the debugger keeps.
// It is intentionally smaller than the customer event ring: a debugger reads
// the most recent transitions, and a 500-entry window is the documented bound.
const DebugRingCapacity = 500

// maxDebugDetailLen bounds the free-text Detail of one entry, so a pathological
// error string cannot grow a retained entry without limit.
const maxDebugDetailLen = 512

// debugSecretValue redacts credentials that the shared events.Scrub value
// pattern does not name, in particular SIDs and device material. Group 1 is the
// label, group 2 the separator, group 3 the value; only the value is replaced,
// so an operator still sees what was scrubbed. The match is deliberately broad:
// a false positive hides a harmless value, a false negative leaks a credential.
var debugSecretValue = regexp.MustCompile(`(?i)\b(sid|session[_-]?id|cookie|set-cookie|authorization|device[_-]?id|machinecode|cuid|osdid|uid|connectpassword|login[_-]?token|access[_-]?token|refresh[_-]?token)(\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s&,;]+)`)

// DebugRecorder is the bounded, redacted trace ring behind /v1/debug. It wraps
// events.Ring so there is one ring implementation, one cursor grammar and one
// subscriber fan-out in the daemon. The zero value is not usable; construct it
// with NewDebugRecorder. A nil *DebugRecorder is a valid no-op recorder, so the
// runtime instrumentation stays unconditional and a test controller does not
// need a trace sink.
type DebugRecorder struct {
	ring *events.Ring
	now  func() time.Time
}

// NewDebugRecorder returns a recorder retaining capacity entries. A
// non-positive capacity selects DebugRingCapacity.
func NewDebugRecorder(capacity int) *DebugRecorder {
	if capacity <= 0 {
		capacity = DebugRingCapacity
	}
	return &DebugRecorder{ring: events.NewRing(capacity), now: time.Now}
}

// Record stamps, scrubs and publishes one trace entry. It returns the stored
// entry with its assigned Seq and At. A recorder that was closed returns the
// zero entry, matching events.Ring.Publish.
//
// Detail and ErrorClass are scrubbed twice: first by the shared events.Scrub
// (key and embedded password/token/secret patterns), then by the debugger
// pattern that also names SIDs, cookies and device material. Identifiers such
// as AccountID and NodeID are not free text and are copied verbatim.
func (r *DebugRecorder) Record(entry model.DebugTrace) model.DebugTrace {
	if r == nil || entry.Kind == "" {
		return model.DebugTrace{}
	}
	entry = sanitizeDebugTrace(entry)
	event := r.ring.Publish(events.Event{
		Type: entry.Kind,
		At:   r.now().UTC(),
		Data: map[string]any{"trace": entry},
	})
	if event.ID == "" {
		return model.DebugTrace{}
	}
	entry.Seq = debugSeq(event.ID)
	entry.At = event.At.UTC().Format(time.RFC3339Nano)
	return entry
}

// Snapshot returns up to limit retained entries for account, oldest first. An
// empty account means every account. A zero or negative limit returns the whole
// retained window. The page also reports the ring bounds and the cursor to
// resume from.
func (r *DebugRecorder) Snapshot(account string, limit int) model.DebugTracePage {
	if r == nil {
		return model.DebugTracePage{Enabled: false, Reason: "debug trace is not configured", Entries: []model.DebugTrace{}}
	}
	all, _, _ := r.ring.Since("", 0)
	entries := make([]model.DebugTrace, 0, len(all))
	for _, event := range all {
		trace, ok := decodeDebugEvent(event)
		if !ok {
			continue
		}
		if account != "" && trace.AccountID != account {
			continue
		}
		entries = append(entries, trace)
	}
	retained := len(entries)
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return model.DebugTracePage{
		Enabled:   true,
		Capacity:  r.ring.Capacity(),
		Retained:  retained,
		Evicted:   r.ring.Evicted(),
		OldestSeq: r.OldestSeq(),
		NextSeq:   r.LatestSeq(),
		Entries:   entries,
	}
}

// Since returns the retained entries recorded after seq, oldest first. A zero
// seq starts at the oldest retained entry. A cursor that has been evicted (or
// belongs to a previous process and is ahead of the ring) restarts from the
// oldest retained entry and reports restarted=true, so a debugger client never
// receives a silent gap.
func (r *DebugRecorder) Since(seq uint64) ([]model.DebugTrace, bool) {
	if r == nil {
		return []model.DebugTrace{}, false
	}
	cursor := ""
	if seq > 0 {
		cursor = fmt.Sprintf("evt-%06d", seq)
	}
	eventsOut, _, err := r.ring.Since(cursor, 0)
	restarted := false
	if err != nil {
		if !errors.Is(err, events.ErrCursorExpired) && !errors.Is(err, events.ErrBadCursor) {
			return []model.DebugTrace{}, false
		}
		eventsOut, _, _ = r.ring.Since("", 0)
		restarted = true
	}
	entries := make([]model.DebugTrace, 0, len(eventsOut))
	for _, event := range eventsOut {
		if trace, ok := decodeDebugEvent(event); ok {
			entries = append(entries, trace)
		}
	}
	return entries, restarted
}

// Subscribe returns a live channel of trace entries and a cancel function.
// Subscribing does not replay; call Since for the retained window. A slow
// subscriber loses entries into the ring's Dropped counter instead of stalling
// the runtime. The channel is closed by cancel or Close.
func (r *DebugRecorder) Subscribe(buffer int) (<-chan model.DebugTrace, func()) {
	if buffer <= 0 {
		buffer = events.DefaultSubscriberBuffer
	}
	if r == nil {
		ch := make(chan model.DebugTrace)
		close(ch)
		return ch, func() {}
	}
	raw, cancelRaw := r.ring.Subscribe(buffer)
	out := make(chan model.DebugTrace, buffer)
	done := make(chan struct{})
	go func() {
		defer close(out)
		for event := range raw {
			trace, ok := decodeDebugEvent(event)
			if !ok {
				continue
			}
			select {
			case out <- trace:
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			close(done)
			cancelRaw()
		})
	}
	return out, cancel
}

// Close stops the recorder and closes every subscriber channel. Later records
// are ignored. Close is idempotent.
func (r *DebugRecorder) Close() {
	if r == nil {
		return
	}
	r.ring.Close()
}

// Capacity returns the fixed ring size.
func (r *DebugRecorder) Capacity() int {
	if r == nil {
		return 0
	}
	return r.ring.Capacity()
}

// Len reports the number of retained entries.
func (r *DebugRecorder) Len() int {
	if r == nil {
		return 0
	}
	return r.ring.Len()
}

// Evicted reports how many entries the ring overwrote since it was created.
func (r *DebugRecorder) Evicted() uint64 {
	if r == nil {
		return 0
	}
	return r.ring.Evicted()
}

// Dropped reports subscriber deliveries skipped because a channel was full.
func (r *DebugRecorder) Dropped() uint64 {
	if r == nil {
		return 0
	}
	return r.ring.Dropped()
}

// LatestSeq returns the newest retained sequence, or 0 when empty.
func (r *DebugRecorder) LatestSeq() uint64 {
	if r == nil {
		return 0
	}
	return debugSeq(r.ring.Latest())
}

// OldestSeq returns the oldest retained sequence, or 0 when empty.
func (r *DebugRecorder) OldestSeq() uint64 {
	if r == nil {
		return 0
	}
	return debugSeq(r.ring.Oldest())
}

// sanitizeDebugTrace scrubs the free-text fields and bounds Detail. The shared
// scrub runs first so the events package stays the single definition of the
// password/token/secret patterns; the debugger pattern then covers SIDs,
// cookies and device material.
func sanitizeDebugTrace(entry model.DebugTrace) model.DebugTrace {
	fields := events.Scrub(map[string]any{
		"detail":     entry.Detail,
		"errorClass": entry.ErrorClass,
	})
	entry.Detail = debugRedact(stringField(fields["detail"]))
	entry.ErrorClass = debugRedact(stringField(fields["errorClass"]))
	if len(entry.Detail) > maxDebugDetailLen {
		entry.Detail = entry.Detail[:maxDebugDetailLen]
	}
	if len(entry.ErrorClass) > maxDebugDetailLen {
		entry.ErrorClass = entry.ErrorClass[:maxDebugDetailLen]
	}
	return entry
}

func stringField(value any) string {
	text, _ := value.(string)
	return text
}

func debugRedact(value string) string {
	if !debugSecretValue.MatchString(value) {
		return value
	}
	return debugSecretValue.ReplaceAllString(value, "${1}${2}"+events.Redacted)
}

// newDebugTrace builds one attributed entry for a runtime boundary. The
// duration is rounded down to milliseconds; a zero duration is omitted from the
// JSON by omitempty.
func newDebugTrace(kind string, key model.SlotKey, generation uint64, nodeID, serverID, step, state, detail, errorClass string, duration time.Duration) model.DebugTrace {
	return model.DebugTrace{
		Kind:       kind,
		AccountID:  key.AccountID,
		NodeID:     nodeID,
		ServerID:   serverID,
		Slot:       key.Index,
		Generation: generation,
		Step:       step,
		State:      state,
		DurationMs: duration.Milliseconds(),
		Detail:     detail,
		ErrorClass: errorClass,
	}
}

// decodeDebugEvent unwraps one ring event into its trace entry.
func decodeDebugEvent(event events.Event) (model.DebugTrace, bool) {
	if event.Data == nil {
		return model.DebugTrace{}, false
	}
	trace, ok := event.Data["trace"].(model.DebugTrace)
	if !ok {
		return model.DebugTrace{}, false
	}
	if trace.Seq == 0 {
		trace.Seq = debugSeq(event.ID)
	}
	if trace.At == "" && !event.At.IsZero() {
		trace.At = event.At.UTC().Format(time.RFC3339Nano)
	}
	return trace, true
}

// debugSeq parses the numeric part of a generated ring ID. A caller-supplied
// or malformed ID maps to 0; the debugger never supplies one.
func debugSeq(id string) uint64 {
	const prefix = "evt-"
	if len(id) <= len(prefix) || id[:len(prefix)] != prefix {
		return 0
	}
	seq, err := strconv.ParseUint(id[len(prefix):], 10, 64)
	if err != nil {
		return 0
	}
	return seq
}
