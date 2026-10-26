package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// WebhookOutboxLimit bounds the pending-delivery outbox. An entry is pending
// from the moment the dispatcher accepts an event until an attempt succeeds,
// the retry budget is exhausted or the dispatcher's cap evicts it. The cap
// keeps state.json proportional to a bounded backlog rather than to uptime.
const WebhookOutboxLimit = 1000

// WebhookOutboxEntry is one accepted-but-unfinished webhook delivery persisted
// in the state snapshot. It is the restart-safe counterpart of the dispatcher's
// queued job: Payload is the exact scrubbed JSON body already sent (or about to
// be), PayloadHash its lowercase hex SHA-256, and Attempts the number of
// attempts that completed with a failure. NextAttemptAt is when the next
// attempt is due, so a restart resumes the backoff instead of restarting it.
//
// Timestamps are RFC 3339 strings, matching every other persisted row. No
// signing secret, header value or unscrubbed event field enters this type: the
// body was scrubbed before the dispatcher accepted the event.
type WebhookOutboxEntry struct {
	ID            string          `json:"id"`
	EndpointID    string          `json:"endpointId"`
	Event         string          `json:"event"`
	PayloadHash   string          `json:"payloadHash"`
	Payload       json.RawMessage `json:"payload"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt string          `json:"nextAttemptAt"`
	CreatedAt     string          `json:"createdAt"`
}

// webhookOutboxKey is the dedupe identity: an event id is only unique per
// endpoint, so both halves form the key. The NUL separator cannot occur in
// either half, which keeps the concatenation unambiguous.
func webhookOutboxKey(endpointID, id string) string { return endpointID + "\x00" + id }

// PutWebhookOutbox inserts or replaces one pending delivery. Replacing by
// endpoint id + event id makes a retried attempt update the existing row
// instead of accumulating duplicates. The row is committed before this
// returns: it is the durable counterpart of the dispatcher queue, so a crash
// must resume the backlog rather than lose it.
//
// The store enforces the same cap as the dispatcher as a hard bound. The
// dispatcher evicts first (and reports that eviction), so this path only
// triggers for a caller that bypasses it; the oldest row is dropped either way.
func (s *Store) PutWebhookOutbox(entry WebhookOutboxEntry) error {
	if err := validateWebhookOutboxEntry(entry); err != nil {
		return err
	}
	// Store the compact form: the snapshot encoder re-indents every byte
	// stream, and a compact body is what the dispatcher hashed and sent.
	payload, err := canonicalWebhookOutboxPayload(entry.Payload)
	if err != nil {
		return invalidf("webhook outbox entry %q has no valid payload: %v", entry.ID, err)
	}
	entry.Payload = payload
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.webhookOutbox {
		if s.webhookOutbox[i].EndpointID == entry.EndpointID && s.webhookOutbox[i].ID == entry.ID {
			s.webhookOutbox[i] = cloneWebhookOutboxEntry(entry)
			if err := s.commitLocked(); err != nil {
				return err
			}
			return nil
		}
	}
	s.webhookOutbox = append(s.webhookOutbox, cloneWebhookOutboxEntry(entry))
	if len(s.webhookOutbox) > WebhookOutboxLimit {
		oldest := 0
		for i := range s.webhookOutbox {
			if webhookOutboxOlder(s.webhookOutbox[i], s.webhookOutbox[oldest]) {
				oldest = i
			}
		}
		s.webhookOutbox = append(s.webhookOutbox[:oldest], s.webhookOutbox[oldest+1:]...)
	}
	if err := s.commitLocked(); err != nil {
		return err
	}
	return nil
}

// RemoveWebhookOutbox deletes one pending delivery. It is idempotent: the
// dispatcher removes an entry after success, dead letter and eviction, and two
// of those paths can race.
func (s *Store) RemoveWebhookOutbox(endpointID, id string) error {
	if strings.TrimSpace(endpointID) == "" || strings.TrimSpace(id) == "" {
		return invalidf("webhook outbox removal needs an endpoint and an event id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.webhookOutbox {
		if s.webhookOutbox[i].EndpointID == endpointID && s.webhookOutbox[i].ID == id {
			s.webhookOutbox = append(s.webhookOutbox[:i], s.webhookOutbox[i+1:]...)
			if err := s.commitLocked(); err != nil {
				return err
			}
			return nil
		}
	}
	return nil
}

// WebhookOutbox returns every pending delivery, oldest first. The result is a
// deep copy, so a caller may sort or filter it without touching the store.
func (s *Store) WebhookOutbox() []WebhookOutboxEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]WebhookOutboxEntry, len(s.webhookOutbox))
	for i, entry := range s.webhookOutbox {
		out[i] = cloneWebhookOutboxEntry(entry)
	}
	sortWebhookOutbox(out)
	return out
}

// WebhookOutboxLen reports how many pending deliveries the store holds.
func (s *Store) WebhookOutboxLen() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.webhookOutbox)
}

// validateWebhookOutboxEntry rejects a row that could not be replayed. The
// payload must be the JSON body the dispatcher sent and the digest must match
// it: a mismatch means the row was corrupted between writes and delivering it
// would send bytes the receiver never signed for.
func validateWebhookOutboxEntry(entry WebhookOutboxEntry) error {
	if strings.TrimSpace(entry.ID) == "" {
		return invalidf("webhook outbox entry has no id")
	}
	if strings.TrimSpace(entry.EndpointID) == "" {
		return invalidf("webhook outbox entry %q has no endpoint", entry.ID)
	}
	if strings.TrimSpace(entry.Event) == "" {
		return invalidf("webhook outbox entry %q has no event", entry.ID)
	}
	if entry.Attempts < 0 {
		return invalidf("webhook outbox entry %q has a negative attempt count", entry.ID)
	}
	if !validRFC3339(entry.CreatedAt) {
		return invalidf("webhook outbox entry %q has a non-RFC3339 createdAt", entry.ID)
	}
	if !validRFC3339(entry.NextAttemptAt) {
		return invalidf("webhook outbox entry %q has a non-RFC3339 nextAttemptAt", entry.ID)
	}
	if len(entry.Payload) == 0 || !json.Valid(entry.Payload) {
		return invalidf("webhook outbox entry %q has no valid payload", entry.ID)
	}
	payload, err := canonicalWebhookOutboxPayload(entry.Payload)
	if err != nil {
		return invalidf("webhook outbox entry %q has no valid payload: %v", entry.ID, err)
	}
	if entry.PayloadHash != webhookOutboxDigest(payload) {
		return invalidf("webhook outbox entry %q has a payload hash mismatch", entry.ID)
	}
	return nil
}

// canonicalWebhookOutboxPayload returns the compact JSON form of body.
// encoding/json's indenting encoder re-indents raw messages embedded in the
// snapshot, so the bytes read back from disk carry whitespace the dispatcher
// never hashed. Compacting makes the digest comparison and the replayed body
// independent of snapshot formatting.
func canonicalWebhookOutboxPayload(payload json.RawMessage) (json.RawMessage, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err != nil {
		return nil, err
	}
	return json.RawMessage(compact.Bytes()), nil
}

// webhookOutboxDigest is the contract shared with webhook.PayloadDigest: the
// lowercase hex SHA-256 of the exact body. The store recomputes it instead of
// trusting the row, so a hand-edited snapshot cannot replay arbitrary bytes.
func webhookOutboxDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// normalizeWebhookOutbox enforces the snapshot invariant: one row per
// endpoint/event key, oldest first, at most WebhookOutboxLimit rows. A
// document written by an older build or edited by hand is trimmed rather than
// rejected, the same way Restore treats the delivery history.
func normalizeWebhookOutbox(entries []WebhookOutboxEntry) []WebhookOutboxEntry {
	if len(entries) == 0 {
		return []WebhookOutboxEntry{}
	}
	kept := make([]WebhookOutboxEntry, 0, min(len(entries), WebhookOutboxLimit))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		key := webhookOutboxKey(entry.EndpointID, entry.ID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		// Validation already proved the body compacts; installing the compact
		// form makes the replayed bytes identical to the hashed bytes.
		entry.Payload, _ = canonicalWebhookOutboxPayload(entry.Payload)
		kept = append(kept, cloneWebhookOutboxEntry(entry))
	}
	sortWebhookOutbox(kept)
	if len(kept) > WebhookOutboxLimit {
		// Keep the newest rows: the oldest are the ones a full outbox drops.
		kept = kept[len(kept)-WebhookOutboxLimit:]
	}
	return kept
}

// validateWebhookOutbox rejects a snapshot whose pending rows cannot be
// replayed as-is. Rows for an unknown endpoint are not rejected here: the
// receiver may have been deleted while an attempt was pending, and the
// dispatcher drops such a row on restore instead of failing the whole panel.
func (snap *Snapshot) validateWebhookOutbox() error {
	for _, entry := range snap.WebhookOutbox {
		if err := validateWebhookOutboxEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

// sortWebhookOutbox orders entries oldest first, with the key as the tie
// breaker so a snapshot written twice from the same set is byte-identical.
func sortWebhookOutbox(entries []WebhookOutboxEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339, entries[i].CreatedAt)
		right, _ := time.Parse(time.RFC3339, entries[j].CreatedAt)
		if !left.Equal(right) {
			return left.Before(right)
		}
		return webhookOutboxKey(entries[i].EndpointID, entries[i].ID) <
			webhookOutboxKey(entries[j].EndpointID, entries[j].ID)
	})
}

func webhookOutboxOlder(left, right WebhookOutboxEntry) bool {
	leftAt, _ := time.Parse(time.RFC3339, left.CreatedAt)
	rightAt, _ := time.Parse(time.RFC3339, right.CreatedAt)
	if !leftAt.Equal(rightAt) {
		return leftAt.Before(rightAt)
	}
	return webhookOutboxKey(left.EndpointID, left.ID) < webhookOutboxKey(right.EndpointID, right.ID)
}

func cloneWebhookOutboxEntry(entry WebhookOutboxEntry) WebhookOutboxEntry {
	entry.Payload = append(json.RawMessage(nil), entry.Payload...)
	return entry
}
