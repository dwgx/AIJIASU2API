package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

// testOutboxEntry builds one pending delivery whose payload and digest agree.
func testOutboxEntry(id, endpointID string, createdAt time.Time) WebhookOutboxEntry {
	body := json.RawMessage(`{"id":"` + id + `","event":"account.login"}`)
	return WebhookOutboxEntry{
		ID:            id,
		EndpointID:    endpointID,
		Event:         "account.login",
		PayloadHash:   webhookOutboxDigest(body),
		Payload:       body,
		NextAttemptAt: createdAt.UTC().Format(time.RFC3339Nano),
		CreatedAt:     createdAt.UTC().Format(time.RFC3339Nano),
	}
}

func outboxIDs(entries []WebhookOutboxEntry) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.ID
	}
	return out
}

// TestWebhookOutboxSurvivesSnapshotReload pins the durability contract: pending
// deliveries, their attempts, due time and exact payload survive a snapshot and
// reload, and a second cycle is byte-identical.
func TestWebhookOutboxSurvivesSnapshotReload(t *testing.T) {
	st := NewEmpty()
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	first := testOutboxEntry("evt-1", "wh-001", base)
	first.Attempts = 2
	first.NextAttemptAt = base.Add(30 * time.Second).Format(time.RFC3339Nano)
	second := testOutboxEntry("evt-2", "wh-002", base.Add(time.Second))
	for _, entry := range []WebhookOutboxEntry{second, first} {
		if err := st.PutWebhookOutbox(entry); err != nil {
			t.Fatalf("put %q: %v", entry.ID, err)
		}
	}

	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !bytes.Contains(data, []byte(`"webhookOutbox"`)) {
		t.Fatal("snapshot does not carry the pending outbox")
	}
	if bytes.Contains(data, []byte(`"secret"`)) {
		t.Fatalf("snapshot carries a secret-shaped field: %s", data)
	}

	restored := NewEmpty()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got := restored.WebhookOutbox()
	if ids := outboxIDs(got); len(ids) != 2 || ids[0] != "evt-1" || ids[1] != "evt-2" {
		t.Fatalf("restored outbox = %v, want oldest first [evt-1 evt-2]", ids)
	}
	if got[0].Attempts != 2 || got[0].NextAttemptAt != first.NextAttemptAt {
		t.Fatalf("restored entry lost its retry state: %+v", got[0])
	}
	if !bytes.Equal(got[0].Payload, first.Payload) || got[0].PayloadHash != first.PayloadHash {
		t.Fatalf("restored payload = %s / %s", got[0].Payload, got[0].PayloadHash)
	}
	if got[1].Attempts != 0 || got[1].CreatedAt != second.CreatedAt {
		t.Fatalf("restored second entry = %+v", got[1])
	}

	again, err := restored.Snapshot()
	if err != nil {
		t.Fatalf("snapshot after restore: %v", err)
	}
	if !bytes.Equal(data, again) {
		t.Fatal("snapshot changed after one reload cycle")
	}
}

// TestWebhookOutboxReplacesByKey proves an entry is keyed by endpoint id plus
// event id: a retried attempt updates its row, while the same event id for
// another receiver is a separate delivery.
func TestWebhookOutboxReplacesByKey(t *testing.T) {
	st := NewEmpty()
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	entry := testOutboxEntry("evt-1", "wh-001", base)
	if err := st.PutWebhookOutbox(entry); err != nil {
		t.Fatalf("first put: %v", err)
	}
	entry.Attempts = 3
	entry.NextAttemptAt = base.Add(time.Minute).Format(time.RFC3339Nano)
	if err := st.PutWebhookOutbox(entry); err != nil {
		t.Fatalf("second put: %v", err)
	}
	if got := st.WebhookOutboxLen(); got != 1 {
		t.Fatalf("rows after a repeated key = %d, want 1", got)
	}
	if got := st.WebhookOutbox()[0]; got.Attempts != 3 {
		t.Fatalf("replaced row attempts = %d, want 3", got.Attempts)
	}

	other := testOutboxEntry("evt-1", "wh-002", base.Add(time.Second))
	if err := st.PutWebhookOutbox(other); err != nil {
		t.Fatalf("put for another endpoint: %v", err)
	}
	if got := st.WebhookOutboxLen(); got != 2 {
		t.Fatalf("rows after the same event id for another endpoint = %d, want 2", got)
	}
}

// TestWebhookOutboxCapDropsOldest proves the store's hard bound: the 1001st
// row evicts the oldest, so a caller that bypasses the dispatcher cannot grow
// the snapshot without limit.
func TestWebhookOutboxCapDropsOldest(t *testing.T) {
	st := NewEmpty()
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= WebhookOutboxLimit+1; i++ {
		entry := testOutboxEntry(fmt.Sprintf("evt-%04d", i), "wh-001", base.Add(time.Duration(i)*time.Second))
		if err := st.PutWebhookOutbox(entry); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	got := st.WebhookOutbox()
	if len(got) != WebhookOutboxLimit {
		t.Fatalf("retained %d rows, want the cap %d", len(got), WebhookOutboxLimit)
	}
	if got[0].ID != "evt-0002" {
		t.Fatalf("oldest retained row = %q, want evt-0002", got[0].ID)
	}
	if got[len(got)-1].ID != fmt.Sprintf("evt-%04d", WebhookOutboxLimit+1) {
		t.Fatalf("newest retained row = %q", got[len(got)-1].ID)
	}
}

// TestWebhookOutboxRemoveIsIdempotent proves the dispatcher's two removal paths
// (completion and eviction) can race without failing.
func TestWebhookOutboxRemoveIsIdempotent(t *testing.T) {
	st := NewEmpty()
	entry := testOutboxEntry("evt-1", "wh-001", time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
	if err := st.PutWebhookOutbox(entry); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := st.RemoveWebhookOutbox("wh-001", "evt-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := st.RemoveWebhookOutbox("wh-001", "evt-1"); err != nil {
		t.Fatalf("second remove: %v", err)
	}
	if got := st.WebhookOutboxLen(); got != 0 {
		t.Fatalf("rows after removal = %d, want 0", got)
	}
	if err := st.RemoveWebhookOutbox("", "evt-1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("remove without an endpoint error = %v, want ErrInvalid", err)
	}
}

// TestWebhookOutboxRejectsUnreplayableRows proves a row that could not be sent
// as-is never enters the store: identity, counters, timestamps, the JSON body
// and the digest are all checked.
func TestWebhookOutboxRejectsUnreplayableRows(t *testing.T) {
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	valid := testOutboxEntry("evt-1", "wh-001", base)

	missingID := valid
	missingID.ID = " "
	missingEndpoint := valid
	missingEndpoint.EndpointID = ""
	missingEvent := valid
	missingEvent.Event = ""
	negative := valid
	negative.Attempts = -1
	badCreated := valid
	badCreated.CreatedAt = "yesterday"
	badNext := valid
	badNext.NextAttemptAt = ""
	emptyPayload := valid
	emptyPayload.Payload = nil
	notJSON := valid
	notJSON.Payload = json.RawMessage(`{"id":`)
	notJSON.PayloadHash = webhookOutboxDigest(notJSON.Payload)
	mismatch := valid
	mismatch.PayloadHash = "deadbeef"

	cases := []struct {
		name  string
		entry WebhookOutboxEntry
	}{
		{"missing id", missingID},
		{"missing endpoint", missingEndpoint},
		{"missing event", missingEvent},
		{"negative attempts", negative},
		{"bad createdAt", badCreated},
		{"missing nextAttemptAt", badNext},
		{"empty payload", emptyPayload},
		{"invalid payload", notJSON},
		{"digest mismatch", mismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := NewEmpty()
			if err := st.PutWebhookOutbox(tc.entry); !errors.Is(err, ErrInvalid) {
				t.Fatalf("PutWebhookOutbox error = %v, want ErrInvalid", err)
			}
			if got := st.WebhookOutboxLen(); got != 0 {
				t.Fatalf("an invalid row was stored (%d rows)", got)
			}
		})
	}
}

// TestWebhookOutboxSnapshotRejectsCorruptRow proves restore refuses a document
// whose pending row would replay bytes that no longer match their digest.
func TestWebhookOutboxSnapshotRejectsCorruptRow(t *testing.T) {
	entry := testOutboxEntry("evt-1", "wh-001", time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
	entry.PayloadHash = "deadbeef"
	data, err := json.Marshal(Snapshot{Version: SnapshotVersion, WebhookOutbox: []WebhookOutboxEntry{entry}})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	st := NewEmpty()
	if err := st.Restore(data); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Restore error = %v, want ErrInvalid", err)
	}
	if got := st.WebhookOutboxLen(); got != 0 {
		t.Fatalf("a rejected snapshot installed %d rows", got)
	}
}

// TestWebhookOutboxRestoreNormalizes proves a hand-edited document converges:
// duplicate keys collapse to one row, the rows are ordered oldest first and an
// over-cap document keeps the newest entries.
func TestWebhookOutboxRestoreNormalizes(t *testing.T) {
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	snap := Snapshot{Version: SnapshotVersion}
	// Newest first on purpose, with a duplicate key.
	snap.WebhookOutbox = append(snap.WebhookOutbox,
		testOutboxEntry("evt-3", "wh-001", base.Add(3*time.Second)),
		testOutboxEntry("evt-2", "wh-001", base.Add(2*time.Second)),
		testOutboxEntry("evt-1", "wh-001", base.Add(time.Second)),
		testOutboxEntry("evt-2", "wh-001", base.Add(2*time.Second)),
	)
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	st := NewEmpty()
	if err := st.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if ids := outboxIDs(st.WebhookOutbox()); len(ids) != 3 || ids[0] != "evt-1" || ids[1] != "evt-2" || ids[2] != "evt-3" {
		t.Fatalf("restored outbox = %v, want [evt-1 evt-2 evt-3]", ids)
	}

	// Over-cap documents keep the newest rows.
	over := Snapshot{Version: SnapshotVersion}
	for i := 1; i <= WebhookOutboxLimit+5; i++ {
		over.WebhookOutbox = append(over.WebhookOutbox,
			testOutboxEntry(fmt.Sprintf("evt-%04d", i), "wh-001", base.Add(time.Duration(i)*time.Second)))
	}
	data, err = json.Marshal(over)
	if err != nil {
		t.Fatalf("marshal over-cap snapshot: %v", err)
	}
	st = NewEmpty()
	if err := st.Restore(data); err != nil {
		t.Fatalf("restore over-cap: %v", err)
	}
	got := st.WebhookOutbox()
	if len(got) != WebhookOutboxLimit {
		t.Fatalf("restored %d rows, want the cap %d", len(got), WebhookOutboxLimit)
	}
	if got[0].ID != "evt-0006" {
		t.Fatalf("oldest restored row = %q, want evt-0006", got[0].ID)
	}
}

// TestWebhookOutboxPayloadIsDeepCopied proves a caller cannot mutate the stored
// row through the slice it passed in or received back.
func TestWebhookOutboxPayloadIsDeepCopied(t *testing.T) {
	st := NewEmpty()
	entry := testOutboxEntry("evt-1", "wh-001", time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
	if err := st.PutWebhookOutbox(entry); err != nil {
		t.Fatalf("put: %v", err)
	}
	entry.Payload[0] = 'X'
	if got := st.WebhookOutbox()[0].Payload; !bytes.HasPrefix(got, []byte(`{"id"`)) {
		t.Fatalf("stored payload changed through the caller's slice: %s", got)
	}
	read := st.WebhookOutbox()[0]
	read.Payload[0] = 'Y'
	if got := st.WebhookOutbox()[0].Payload; !bytes.HasPrefix(got, []byte(`{"id"`)) {
		t.Fatalf("stored payload changed through a read copy: %s", got)
	}
}
