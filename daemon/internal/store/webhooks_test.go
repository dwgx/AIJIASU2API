package store

import (
	"testing"

	"ajsaccountd/internal/model"
)

func TestWebhookEndpointLifecycleAndSnapshot(t *testing.T) {
	st := NewEmpty()
	enabled := true
	created, err := st.CreateWebhookEndpoint(model.WebhookEndpointInput{
		URL:         "https://example.com/hook",
		Events:      []string{"account.login", "account.login", " node.connected "},
		Description: " test receiver ",
		Enabled:     &enabled,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID != "wh-001" {
		t.Fatalf("id = %q, want wh-001", created.ID)
	}
	if len(created.Events) != 2 || created.Events[0] != "account.login" || created.Events[1] != "node.connected" {
		t.Fatalf("events = %v, want deduped and trimmed", created.Events)
	}
	if created.Description != "test receiver" || !created.Enabled || created.CreatedAt == "" {
		t.Fatalf("created endpoint = %+v", created)
	}

	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := NewEmpty()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got := restored.WebhookEndpoints()
	if len(got) != 1 || got[0].ID != created.ID || got[0].URL != created.URL {
		t.Fatalf("restored endpoints = %+v", got)
	}

	// The sequence survives the snapshot, so a restored store never reuses an
	// id a receiver may already have configured.
	next, err := restored.CreateWebhookEndpoint(model.WebhookEndpointInput{URL: "https://example.com/next"})
	if err != nil {
		t.Fatalf("create after restore: %v", err)
	}
	if next.ID != "wh-002" {
		t.Fatalf("id after restore = %q, want wh-002", next.ID)
	}

	if _, err := restored.SetWebhookEndpointEnabled("wh-001", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if endpoint, _ := restored.WebhookEndpoint("wh-001"); endpoint.Enabled {
		t.Fatalf("disable did not persist: %+v", endpoint)
	}

	if err := restored.DeleteWebhookEndpoint("wh-001"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := restored.WebhookEndpoint("wh-001"); ok {
		t.Fatal("deleted endpoint still resolves")
	}
	if err := restored.DeleteWebhookEndpoint("wh-001"); err == nil {
		t.Fatal("second delete did not report not-found")
	}
}
