package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"ajsaccountd/internal/model"
)

func testDelivery(id, endpoint string, at time.Time) model.WebhookDelivery {
	return model.WebhookDelivery{
		ID:         id,
		EndpointID: endpoint,
		Event:      "account.login",
		Attempt:    1,
		Status:     "success",
		HTTPStatus: 200,
		At:         at.UTC().Format(time.RFC3339Nano),
		DurationMs: 3,
	}
}

func deliveryTimes(t *testing.T, deliveries []model.WebhookDelivery) []time.Time {
	t.Helper()
	out := make([]time.Time, len(deliveries))
	for i, delivery := range deliveries {
		at, err := time.Parse(time.RFC3339, delivery.At)
		if err != nil {
			t.Fatalf("delivery %q has an unparseable timestamp %q: %v", delivery.ID, delivery.At, err)
		}
		out[i] = at
	}
	return out
}

func TestWebhookDeliveryPerEndpointCapKeepsNewest(t *testing.T) {
	st := NewEmpty()
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= WebhookDeliveriesPerEndpoint+10; i++ {
		delivery := testDelivery(fmt.Sprintf("evt-1:%d", i), "wh-001", base.Add(time.Duration(i)*time.Second))
		if err := st.AppendWebhookDelivery(delivery); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	got := st.WebhookDeliveries("wh-001")
	if len(got) != WebhookDeliveriesPerEndpoint {
		t.Fatalf("retained %d rows, want %d", len(got), WebhookDeliveriesPerEndpoint)
	}
	if got[0].ID != "evt-1:60" {
		t.Fatalf("newest row = %q, want evt-1:60", got[0].ID)
	}
	if got[len(got)-1].ID != "evt-1:11" {
		t.Fatalf("oldest retained row = %q, want evt-1:11", got[len(got)-1].ID)
	}
	times := deliveryTimes(t, got)
	for i := 1; i < len(times); i++ {
		if !times[i-1].After(times[i]) {
			t.Fatalf("rows are not newest first at %d: %s then %s", i, times[i-1], times[i])
		}
	}

	// Another endpoint has its own window and cannot evict this one.
	if err := st.AppendWebhookDelivery(testDelivery("evt-2:1", "wh-002", base.Add(time.Hour))); err != nil {
		t.Fatalf("append other endpoint: %v", err)
	}
	if got := st.WebhookDeliveries("wh-002"); len(got) != 1 {
		t.Fatalf("second endpoint retained %d rows, want 1", len(got))
	}
	if got := st.WebhookDeliveries("wh-001"); len(got) != WebhookDeliveriesPerEndpoint {
		t.Fatalf("first endpoint retained %d rows after a foreign append, want %d", len(got), WebhookDeliveriesPerEndpoint)
	}
}

func TestWebhookDeliveryGlobalCapDropsOldest(t *testing.T) {
	st := NewEmpty()
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	const endpoints = 11 // 11 * 50 = 550, so the global cap must evict 50
	sequence := 0
	for e := 1; e <= endpoints; e++ {
		endpoint := fmt.Sprintf("wh-%03d", e)
		for range WebhookDeliveriesPerEndpoint {
			sequence++
			delivery := testDelivery(fmt.Sprintf("evt-%d:1", sequence), endpoint, base.Add(time.Duration(sequence)*time.Second))
			if err := st.AppendWebhookDelivery(delivery); err != nil {
				t.Fatalf("append %d: %v", sequence, err)
			}
		}
	}

	total := 0
	for e := 1; e <= endpoints; e++ {
		rows := st.WebhookDeliveries(fmt.Sprintf("wh-%03d", e))
		if len(rows) > WebhookDeliveriesPerEndpoint {
			t.Fatalf("endpoint %d retained %d rows, over the per-endpoint cap", e, len(rows))
		}
		total += len(rows)
	}
	if total != WebhookDeliveryRetention {
		t.Fatalf("retained %d rows, want the global cap %d", total, WebhookDeliveryRetention)
	}
	// The first endpoint's rows are the 50 oldest, so the global cap drops
	// exactly that window.
	if rows := st.WebhookDeliveries("wh-001"); len(rows) != 0 {
		t.Fatalf("oldest endpoint retained %d rows, want 0", len(rows))
	}
	if rows := st.WebhookDeliveries("wh-011"); len(rows) != WebhookDeliveriesPerEndpoint {
		t.Fatalf("newest endpoint retained %d rows, want %d", len(rows), WebhookDeliveriesPerEndpoint)
	}
}

func TestWebhookDeliveriesSurviveSnapshotReload(t *testing.T) {
	st := NewEmpty()
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	first, err := st.CreateWebhookEndpoint(model.WebhookEndpointInput{URL: "https://example.com/hook"})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	second, err := st.CreateWebhookEndpoint(model.WebhookEndpointInput{URL: "https://example.com/other"})
	if err != nil {
		t.Fatalf("create second endpoint: %v", err)
	}
	for i := 1; i <= WebhookDeliveriesPerEndpoint+5; i++ {
		delivery := testDelivery(fmt.Sprintf("evt-1:%d", i), first.ID, base.Add(time.Duration(i)*time.Second))
		delivery.Error = "http status 500"
		delivery.Status = "failed"
		if err := st.AppendWebhookDelivery(delivery); err != nil {
			t.Fatalf("append first %d: %v", i, err)
		}
	}
	if err := st.AppendWebhookDelivery(testDelivery("evt-2:1", second.ID, base.Add(2*time.Hour))); err != nil {
		t.Fatalf("append second: %v", err)
	}

	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !bytes.Contains(data, []byte(`"webhookDeliveries"`)) {
		t.Fatal("snapshot does not carry the delivery history")
	}

	restored := NewEmpty()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got := restored.WebhookDeliveries(first.ID)
	if len(got) != WebhookDeliveriesPerEndpoint {
		t.Fatalf("restored %d rows, want %d", len(got), WebhookDeliveriesPerEndpoint)
	}
	if got[0].ID != "evt-1:55" || got[len(got)-1].ID != "evt-1:6" {
		t.Fatalf("restored window = %q..%q, want evt-1:55..evt-1:6", got[0].ID, got[len(got)-1].ID)
	}
	if got[0].Status != "failed" || got[0].Error != "http status 500" || got[0].HTTPStatus != 200 || got[0].DurationMs != 3 {
		t.Fatalf("restored row lost fields: %+v", got[0])
	}
	if times := deliveryTimes(t, got); !times[0].After(times[len(times)-1]) {
		t.Fatalf("restored rows are not newest first: %s..%s", times[0], times[len(times)-1])
	}
	if other := restored.WebhookDeliveries(second.ID); len(other) != 1 || other[0].ID != "evt-2:1" {
		t.Fatalf("second endpoint restored as %+v", other)
	}

	// A second save/reload cycle is byte-identical: the history is stable.
	again, err := restored.Snapshot()
	if err != nil {
		t.Fatalf("snapshot after restore: %v", err)
	}
	if !bytes.Equal(data, again) {
		t.Fatal("snapshot changed after one reload cycle")
	}
}

func TestWebhookDeliveryRestoreNormalizesCapsAndOrder(t *testing.T) {
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	const endpoints = 12 // 12 * 50 = 600 rows, so the global cap must bite
	snap := Snapshot{Version: SnapshotVersion}
	sequence := 0
	// Oldest first on purpose: restore must reorder to newest first.
	for e := 1; e <= endpoints; e++ {
		for range WebhookDeliveriesPerEndpoint {
			sequence++
			snap.WebhookDeliveries = append(snap.WebhookDeliveries,
				testDelivery(fmt.Sprintf("evt-%d:1", sequence), fmt.Sprintf("wh-%03d", e), base.Add(time.Duration(sequence)*time.Second)))
		}
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	st := NewEmpty()
	if err := st.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}

	total := 0
	var newest, oldest time.Time
	for e := 1; e <= endpoints; e++ {
		rows := st.WebhookDeliveries(fmt.Sprintf("wh-%03d", e))
		if len(rows) > WebhookDeliveriesPerEndpoint {
			t.Fatalf("endpoint %d retained %d rows, over the per-endpoint cap", e, len(rows))
		}
		total += len(rows)
		for _, at := range deliveryTimes(t, rows) {
			if newest.IsZero() || at.After(newest) {
				newest = at
			}
			if oldest.IsZero() || at.Before(oldest) {
				oldest = at
			}
		}
	}
	if total != WebhookDeliveryRetention {
		t.Fatalf("restored %d rows, want the global cap %d", total, WebhookDeliveryRetention)
	}
	// The 600th append is the newest row and must survive; the first is the
	// oldest and must be gone.
	if !newest.Equal(base.Add(time.Duration(sequence) * time.Second)) {
		t.Fatalf("newest restored row is %s, want the last appended", newest)
	}
	if !oldest.After(base.Add(time.Second)) {
		t.Fatalf("oldest restored row is %s, want the oldest rows evicted", oldest)
	}
	for e := 1; e <= endpoints; e++ {
		for _, delivery := range st.WebhookDeliveries(fmt.Sprintf("wh-%03d", e)) {
			if delivery.ID == "evt-1:1" {
				t.Fatal("restore kept the oldest delivery")
			}
		}
	}
}

func TestWebhookDeliverySnapshotRejectsInvalidRows(t *testing.T) {
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	noEndpoint := testDelivery("evt-1:2", "", base)
	noEvent := testDelivery("evt-1:3", "wh-001", base)
	noEvent.Event = " "
	noStatus := testDelivery("evt-1:4", "wh-001", base)
	noStatus.Status = ""
	badTime := testDelivery("evt-1:5", "wh-001", base)
	badTime.At = "yesterday"
	negative := testDelivery("evt-1:6", "wh-001", base)
	negative.DurationMs = -1

	cases := []struct {
		name string
		rows []model.WebhookDelivery
	}{
		{name: "missing endpoint", rows: []model.WebhookDelivery{noEndpoint}},
		{name: "missing event", rows: []model.WebhookDelivery{noEvent}},
		{name: "missing status", rows: []model.WebhookDelivery{noStatus}},
		{name: "bad timestamp", rows: []model.WebhookDelivery{badTime}},
		{name: "negative duration", rows: []model.WebhookDelivery{negative}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(Snapshot{Version: SnapshotVersion, WebhookDeliveries: tc.rows})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			st := NewEmpty()
			if err := st.Restore(data); err == nil {
				t.Fatal("restore accepted an invalid delivery row")
			}
			if rows := st.WebhookDeliveries("wh-001"); len(rows) != 0 {
				t.Fatalf("failed restore left %d rows behind", len(rows))
			}
		})
	}

	if err := NewEmpty().AppendWebhookDelivery(badTime); err == nil {
		t.Fatal("append accepted a non-RFC3339 timestamp")
	}
}

func TestDeleteWebhookEndpointDropsItsDeliveries(t *testing.T) {
	st := NewEmpty()
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	endpoint, err := st.CreateWebhookEndpoint(model.WebhookEndpointInput{URL: "https://example.com/hook"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.AppendWebhookDelivery(testDelivery("evt-1:1", endpoint.ID, base)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := st.DeleteWebhookEndpoint(endpoint.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rows := st.WebhookDeliveries(endpoint.ID); len(rows) != 0 {
		t.Fatalf("deleted endpoint kept %d delivery rows", len(rows))
	}
}

func TestWebhookDeliveryConcurrentAppendsStayBounded(t *testing.T) {
	st := NewEmpty()
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	const workers = 8
	const perWorker = 100
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			endpoint := fmt.Sprintf("wh-%03d", worker+1)
			for i := range perWorker {
				delivery := testDelivery(
					fmt.Sprintf("evt-%d:%d", worker, i),
					endpoint,
					base.Add(time.Duration(worker*perWorker+i)*time.Second),
				)
				if err := st.AppendWebhookDelivery(delivery); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	total := 0
	for w := range workers {
		rows := st.WebhookDeliveries(fmt.Sprintf("wh-%03d", w+1))
		if len(rows) != WebhookDeliveriesPerEndpoint {
			t.Fatalf("endpoint %d retained %d rows, want %d", w+1, len(rows), WebhookDeliveriesPerEndpoint)
		}
		times := deliveryTimes(t, rows)
		for i := 1; i < len(times); i++ {
			if !times[i-1].After(times[i]) {
				t.Fatalf("endpoint %d is not newest first at %d", w+1, i)
			}
		}
		total += len(rows)
	}
	if total != workers*WebhookDeliveriesPerEndpoint {
		t.Fatalf("retained %d rows, want %d", total, workers*WebhookDeliveriesPerEndpoint)
	}
}

func TestWebhookDeliveriesReturnsCopy(t *testing.T) {
	st := NewEmpty()
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	if err := st.AppendWebhookDelivery(testDelivery("evt-1:1", "wh-001", base)); err != nil {
		t.Fatalf("append: %v", err)
	}
	rows := st.WebhookDeliveries("wh-001")
	rows[0].ID = "tampered"
	if got := st.WebhookDeliveries("wh-001"); got[0].ID != "evt-1:1" {
		t.Fatalf("store row was mutated through the result: %q", got[0].ID)
	}
}

func TestWebhookDeliveryReusedIDReplacesRow(t *testing.T) {
	st := NewEmpty()
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	first := testDelivery("evt-1:1", "wh-001", base)
	first.Status = "failed"
	if err := st.AppendWebhookDelivery(first); err != nil {
		t.Fatalf("append first: %v", err)
	}
	// The dispatcher's dedupe window only covers 60 s; a producer that reuses
	// an event id must not leave two rows with the same delivery id.
	retry := testDelivery("evt-1:1", "wh-001", base.Add(time.Minute))
	if err := st.AppendWebhookDelivery(retry); err != nil {
		t.Fatalf("append retry: %v", err)
	}
	rows := st.WebhookDeliveries("wh-001")
	if len(rows) != 1 {
		t.Fatalf("reused id kept %d rows, want 1", len(rows))
	}
	if rows[0].At != retry.At {
		t.Fatalf("reused id kept the old row: %+v", rows[0])
	}

	// A hand-edited snapshot with a repeated id collapses to the newest copy
	// instead of failing the load.
	snap := Snapshot{Version: SnapshotVersion, WebhookDeliveries: []model.WebhookDelivery{first, retry}}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	restored := NewEmpty()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rows = restored.WebhookDeliveries("wh-001")
	if len(rows) != 1 || rows[0].At != retry.At {
		t.Fatalf("restored duplicate rows = %+v, want the newest copy", rows)
	}
}
