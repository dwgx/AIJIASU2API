package webhook

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignKnownVector(t *testing.T) {
	body := []byte(`{"a":1}`)
	const want = "a438e398bfafc57e4396bb7fc2304422f0f768e965d073ca313cb52e22e6ad03"
	if got := Sign("key", "1700000000", body); got != want {
		t.Fatalf("Sign() = %q, want %q", got, want)
	}
}

func TestSignatureAndVerify(t *testing.T) {
	body := []byte(`{"id":"e1"}`)
	at := time.Unix(1700000000, 0).UTC()
	signature, timestamp := Signature("key", at, body)
	if timestamp != "1700000000" {
		t.Fatalf("timestamp = %q", timestamp)
	}
	if !strings.HasPrefix(signature, SignaturePrefix) {
		t.Fatalf("signature %q lacks %q", signature, SignaturePrefix)
	}
	if !VerifySignature("key", timestamp, body, signature) {
		t.Fatal("valid signature rejected")
	}
	if VerifySignature("other", timestamp, body, signature) {
		t.Fatal("signature accepted with the wrong secret")
	}
	if VerifySignature("key", timestamp, []byte(`{"id":"e2"}`), signature) {
		t.Fatal("signature accepted for a tampered body")
	}
	if VerifySignature("key", "1700000001", body, signature) {
		t.Fatal("signature accepted for a different timestamp")
	}
	if VerifySignature("key", timestamp, body, strings.TrimPrefix(signature, SignaturePrefix)) {
		t.Fatal("signature accepted without the sha256= prefix")
	}
}

func TestScrubRedactsNestedSensitiveKeys(t *testing.T) {
	original := map[string]any{
		"account":     "acct-1",
		"token":       "tok-123",
		"Secret":      "shh",
		"machineCode": "mc-1",
		"identity":    "id-1",
		"nested":      map[string]any{"password": "pw", "label": "keep"},
		"headers":     map[string]string{"Authorization": "Bearer x", "Accept": "json"},
		"list":        []any{map[string]any{"cookie": "c=1", "name": "n"}, "plain"},
		"mapList":     []map[string]any{{"renewalToken": "rt", "ok": true}},
		"count":       3,
	}

	scrubbed := Scrub(original)
	for _, key := range []string{"token", "Secret", "machineCode", "identity"} {
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
	if scrubbed["account"] != "acct-1" || scrubbed["count"] != 3 {
		t.Fatalf("benign fields changed: %#v", scrubbed)
	}

	// The caller's map must be untouched: a dispatcher never mutates input.
	if original["token"] != "tok-123" || original["nested"].(map[string]any)["password"] != "pw" {
		t.Fatalf("Scrub mutated the input: %#v", original)
	}
	if Scrub(nil) != nil {
		t.Fatal("Scrub(nil) must be nil")
	}
}

func TestEndpointValidation(t *testing.T) {
	valid := Endpoint{ID: "ep", URL: "https://example.com/hook", Secret: "s", Enabled: true}
	if err := validateEndpoint(valid, false); err != nil {
		t.Fatalf("valid https endpoint rejected: %v", err)
	}
	cases := []struct {
		name      string
		endpoint  Endpoint
		allowHTTP bool
		want      bool
	}{
		{"missing id", Endpoint{URL: "https://example.com", Secret: "s"}, false, false},
		{"missing secret", Endpoint{ID: "ep", URL: "https://example.com"}, false, false},
		{"http remote", Endpoint{ID: "ep", URL: "http://example.com/hook", Secret: "s"}, false, false},
		{"http remote allowed", Endpoint{ID: "ep", URL: "http://example.com/hook", Secret: "s"}, true, true},
		{"http loopback", Endpoint{ID: "ep", URL: "http://127.0.0.1:8080/hook", Secret: "s"}, false, true},
		{"http ipv6 loopback", Endpoint{ID: "ep", URL: "http://[::1]:8080/hook", Secret: "s"}, false, true},
		{"http localhost", Endpoint{ID: "ep", URL: "http://localhost:8080/hook", Secret: "s"}, false, true},
		{"ftp", Endpoint{ID: "ep", URL: "ftp://example.com", Secret: "s"}, false, false},
		{"userinfo", Endpoint{ID: "ep", URL: "https://user:pass@example.com", Secret: "s"}, false, false},
		{"unknown event", Endpoint{ID: "ep", URL: "https://example.com", Secret: "s", Events: []EventType{"nope"}}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEndpoint(tc.endpoint, tc.allowHTTP)
			if tc.want && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.want && !errors.Is(err, ErrInvalidEndpoint) {
				t.Fatalf("error = %v, want ErrInvalidEndpoint", err)
			}
		})
	}
}

func TestDeliveryLogRingDropsOldest(t *testing.T) {
	log := NewDeliveryLog(3)
	for i := 0; i < 5; i++ {
		log.Append(Delivery{
			ID:         string(rune('a' + i)),
			EndpointID: "ep1",
			Attempt:    i + 1,
		})
	}
	recent := log.Recent()
	if len(recent) != 3 {
		t.Fatalf("global len = %d, want 3", len(recent))
	}
	if recent[0].ID != "c" || recent[2].ID != "e" {
		t.Fatalf("global ring = %v", recent)
	}
	perEndpoint := log.RecentFor("ep1")
	if len(perEndpoint) != 3 || perEndpoint[0].ID != "c" {
		t.Fatalf("endpoint ring = %v", perEndpoint)
	}
	if log.Retries() != 4 {
		t.Fatalf("retries = %d, want 4", log.Retries())
	}
	if len(log.RecentFor("missing")) != 0 {
		t.Fatal("unknown endpoint must have an empty ring")
	}
}

func TestDeliveryLogDefaultCapacity(t *testing.T) {
	log := NewDeliveryLog(0)
	for i := 0; i < DefaultRingSize+25; i++ {
		log.Append(Delivery{ID: "d", EndpointID: "ep"})
	}
	if got := log.Len(); got != DefaultRingSize {
		t.Fatalf("len = %d, want %d", got, DefaultRingSize)
	}
}
