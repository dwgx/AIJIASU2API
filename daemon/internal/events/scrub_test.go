package events

import (
	"fmt"
	"strings"
	"testing"
)

func TestScrubRedactsNestedSensitiveKeys(t *testing.T) {
	original := map[string]any{
		"account":     "acct-1",
		"token":       "tok-123",
		"Secret":      "shh",
		"machineCode": "mc-1",
		"identity":    "id-1",
		"passwd":      "pw-1",
		"apiKey":      "ak-1",
		"api_key":     "ak-2",
		"credential":  "cred-1",
		"private_key": "pk-1",
		"session":     "sess-1",
		"bearer":      "bear-1",
		"nested":      map[string]any{"password": "pw", "label": "keep"},
		"headers":     map[string]string{"Authorization": "Bearer x", "Accept": "json"},
		"list":        []any{map[string]any{"cookie": "c=1", "name": "n"}, "plain"},
		"mapList":     []map[string]any{{"renewalToken": "rt", "ok": true}},
		"strings":     []string{"keep", "secret=inline-value"},
		"count":       3,
		"message":     "login failed password=hunter2 for bob",
		"query":       "https://x.test/cb?api_key=abc123&ok=1",
		"quoted":      `token: "two words" trailing`,
		"benign":      "nothing to hide here",
	}

	scrubbed := Scrub(original)
	for _, key := range []string{
		"token", "Secret", "machineCode", "identity", "passwd", "apiKey",
		"api_key", "credential", "private_key", "session", "bearer",
	} {
		if scrubbed[key] != Redacted {
			t.Fatalf("%s = %v, want %s", key, scrubbed[key], Redacted)
		}
	}
	nested := scrubbed["nested"].(map[string]any)
	if nested["password"] != Redacted || nested["label"] != "keep" {
		t.Fatalf("nested = %#v", nested)
	}
	headers := scrubbed["headers"].(map[string]string)
	if headers["Authorization"] != Redacted || headers["Accept"] != "json" {
		t.Fatalf("headers = %#v", headers)
	}
	list := scrubbed["list"].([]any)
	item := list[0].(map[string]any)
	if item["cookie"] != Redacted || item["name"] != "n" {
		t.Fatalf("list item = %#v", item)
	}
	if list[1] != "plain" {
		t.Fatalf("scalar list item = %#v", list[1])
	}
	mapList := scrubbed["mapList"].([]map[string]any)
	if mapList[0]["renewalToken"] != Redacted || mapList[0]["ok"] != true {
		t.Fatalf("mapList = %#v", mapList)
	}
	stringList := scrubbed["strings"].([]string)
	if stringList[0] != "keep" || stringList[1] != "secret="+Redacted {
		t.Fatalf("string slice = %#v", stringList)
	}

	// Values that embed a credential assignment are redacted, but the label
	// and the rest of the string stay readable.
	if got := scrubbed["message"]; got != "login failed password="+Redacted+" for bob" {
		t.Fatalf("message = %q", got)
	}
	if got := scrubbed["query"]; got != "https://x.test/cb?api_key="+Redacted+"&ok=1" {
		t.Fatalf("query = %q", got)
	}
	if got := scrubbed["quoted"]; got != `token: `+Redacted+` trailing` {
		t.Fatalf("quoted = %q", got)
	}
	if scrubbed["benign"] != "nothing to hide here" {
		t.Fatalf("benign string changed: %q", scrubbed["benign"])
	}
	if strings.Contains(scrubbed["message"].(string), "hunter2") ||
		strings.Contains(scrubbed["query"].(string), "abc123") ||
		strings.Contains(scrubbed["quoted"].(string), "two words") {
		t.Fatalf("embedded credential survived: %#v", scrubbed)
	}

	if scrubbed["account"] != "acct-1" || scrubbed["count"] != 3 {
		t.Fatalf("benign fields changed: %#v", scrubbed)
	}

	// The caller's map must be untouched: Publish never mutates input.
	if original["token"] != "tok-123" || original["nested"].(map[string]any)["password"] != "pw" {
		t.Fatalf("Scrub mutated the input: %#v", original)
	}
	if Scrub(nil) != nil {
		t.Fatal("Scrub(nil) must be nil")
	}
}

func TestPublishScrubsData(t *testing.T) {
	ring := NewRing(4)
	defer ring.Close()

	original := map[string]any{
		"account": "acct-1",
		"token":   "tok-123",
		"nested":  map[string]any{"password": "hunter2", "label": "keep"},
		"message": "login failed password=hunter2 for bob",
		"count":   3,
	}
	published := ring.Publish(Event{Type: "account.login", Data: original})
	if published.Data["token"] != Redacted {
		t.Fatalf("returned Data token = %v, want %s", published.Data["token"], Redacted)
	}
	if original["token"] != "tok-123" {
		t.Fatalf("Publish mutated the caller's map: %#v", original)
	}

	events, _, err := ring.Since("", 1)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("replayed %d events, want 1", len(events))
	}
	stored := events[0].Data
	if stored["token"] != Redacted {
		t.Fatalf("stored token = %v, want %s", stored["token"], Redacted)
	}
	nested := stored["nested"].(map[string]any)
	if nested["password"] != Redacted || nested["label"] != "keep" {
		t.Fatalf("stored nested = %#v", nested)
	}
	if got := stored["message"]; got != "login failed password="+Redacted+" for bob" {
		t.Fatalf("stored message = %q", got)
	}
	if stored["account"] != "acct-1" || stored["count"] != 3 {
		t.Fatalf("benign fields changed: %#v", stored)
	}
	if rendered := fmt.Sprint(stored); strings.Contains(rendered, "tok-123") ||
		strings.Contains(rendered, "hunter2") {
		t.Fatalf("credential reached the ring: %s", rendered)
	}
}
