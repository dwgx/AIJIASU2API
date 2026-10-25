package webhook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// DefaultOutboxLimit bounds the durable pending-delivery set. An entry stays
// in the outbox from the moment Dispatch accepts the event until an attempt
// succeeds, the retry budget is exhausted, or the entry is evicted. The cap
// keeps the snapshot, which is rewritten whole on every save, proportional to
// a bounded backlog instead of to uptime.
const DefaultOutboxLimit = 1000

// OutboxEntry is one accepted-but-unfinished delivery. It is the restart-safe
// counterpart of a queued job: the dispatcher removes it only after the
// delivery finishes, so a restart resumes exactly the set that was still
// pending.
//
// Payload is the exact JSON body sent to the receiver and PayloadHash is its
// lowercase hex SHA-256. The body is built once, from Scrub'd event data, and
// replayed byte-for-byte after a restart, so a receiver observes the same
// bytes and can verify the same signature material either way. The event data
// is scrubbed before the entry exists, so a credential a caller put in an
// event map is already [REDACTED] in the persisted payload.
//
// Attempts counts the attempts that completed with a failure, so the next
// attempt is Attempts+1. NextAttemptAt is when that attempt is due: a restored
// entry sleeps out the remainder of its backoff before it is retried.
type OutboxEntry struct {
	ID            string          `json:"id"`
	EndpointID    string          `json:"endpointId"`
	Event         EventType       `json:"event"`
	PayloadHash   string          `json:"payloadHash"`
	Payload       json.RawMessage `json:"payload"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt time.Time       `json:"nextAttemptAt"`
	CreatedAt     time.Time       `json:"createdAt"`
}

// Key is the dedupe identity of an entry. An event id is only unique per
// endpoint — one producer event fans out to several receivers — so both halves
// form the key. The NUL separator cannot appear in an endpoint id or an event
// id, which keeps the concatenation unambiguous.
func (e OutboxEntry) Key() string { return e.EndpointID + "\x00" + e.ID }

// PayloadDigest returns the lowercase hex SHA-256 of body, the value
// OutboxEntry.PayloadHash carries. A restored entry whose digest no longer
// matches its payload is corrupt and is never delivered.
func PayloadDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// OutboxStore is the durable side of the outbox. The dispatcher calls it from
// delivery workers, so implementations must be fast (an in-memory append) and
// must never call back into the dispatcher. Errors are logged and the delivery
// continues: a persistence fault degrades durability, never delivery.
type OutboxStore interface {
	// PutOutbox inserts or replaces the entry keyed by endpoint id + event id.
	PutOutbox(entry OutboxEntry) error
	// RemoveOutbox deletes the entry and is idempotent.
	RemoveOutbox(endpointID, id string) error
	// Outbox returns every pending entry, oldest first.
	Outbox() []OutboxEntry
}

// outboxRecord is the dispatcher's live view of one pending delivery. Jobs
// hold the pointer they were queued with, so an entry evicted by the cap and
// later re-accepted under the same key cannot be completed or advanced by the
// stale job: every mutation re-checks that the map still holds this record.
type outboxRecord struct {
	entry OutboxEntry
}
