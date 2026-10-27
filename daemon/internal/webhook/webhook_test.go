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

	// The caller's map must be untouched: a dispatcher never mutates input.
	if original["token"] != "tok-123" || original["nested"].(map[string]any)["password"] != "pw" {
		t.Fatalf("Scrub mutated the input: %#v", original)
	}
	if Scrub(nil) != nil {
		t.Fatal("Scrub(nil) must be nil")
	}
}

func TestEndpointValidation(t *testing.T) {
	valid := Endpoint{ID: "ep", URL: "https://93.184.216.34/hook", Secret: "s", Enabled: true}
	if err := validateEndpoint(valid, false, false); err != nil {
		t.Fatalf("valid https endpoint rejected: %v", err)
	}
	cases := []struct {
		name         string
		endpoint     Endpoint
		allowHTTP    bool
		allowPrivate bool
		want         bool
	}{
		{"missing id", Endpoint{URL: "https://93.184.216.34", Secret: "s"}, false, false, false},
		{"missing secret", Endpoint{ID: "ep", URL: "https://93.184.216.34"}, false, false, false},
		{"http remote", Endpoint{ID: "ep", URL: "http://93.184.216.34/hook", Secret: "s"}, false, false, false},
		{"http remote allowed", Endpoint{ID: "ep", URL: "http://93.184.216.34/hook", Secret: "s"}, true, false, true},
		{"https loopback", Endpoint{ID: "ep", URL: "https://127.0.0.1:8080/hook", Secret: "s"}, false, false, false},
		{"https private", Endpoint{ID: "ep", URL: "https://10.0.0.5/hook", Secret: "s"}, false, false, false},
		{"http loopback", Endpoint{ID: "ep", URL: "http://127.0.0.1:8080/hook", Secret: "s"}, false, false, false},
		// The dial guard refuses loopback unless private access is enabled, so
		// validation refuses it too, including the localhost name that the
		// scheme rule alone would admit and even with the HTTP opt-in.
		{"https localhost", Endpoint{ID: "ep", URL: "https://localhost:8443/hook", Secret: "s"}, false, false, false},
		{"http localhost", Endpoint{ID: "ep", URL: "http://localhost:8080/hook", Secret: "s"}, false, false, false},
		{"http localhost scheme opt-in", Endpoint{ID: "ep", URL: "http://localhost:8080/hook", Secret: "s"}, true, false, false},
		{"http loopback lab", Endpoint{ID: "ep", URL: "http://127.0.0.1:8080/hook", Secret: "s"}, false, true, true},
		{"http ipv6 loopback lab", Endpoint{ID: "ep", URL: "http://[::1]:8080/hook", Secret: "s"}, false, true, true},
		{"http localhost lab", Endpoint{ID: "ep", URL: "http://localhost:8080/hook", Secret: "s"}, false, true, true},
		{"https localhost lab", Endpoint{ID: "ep", URL: "https://localhost:8443/hook", Secret: "s"}, false, true, true},
		{"metadata with lab opt-in", Endpoint{ID: "ep", URL: "https://metadata.google.internal/hook", Secret: "s"}, false, true, false},
		{"ftp", Endpoint{ID: "ep", URL: "ftp://93.184.216.34", Secret: "s"}, false, false, false},
		{"userinfo", Endpoint{ID: "ep", URL: "https://user:pass@93.184.216.34", Secret: "s"}, false, false, false},
		{"unknown event", Endpoint{ID: "ep", URL: "https://93.184.216.34", Secret: "s", Events: []EventType{"nope"}}, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEndpoint(tc.endpoint, tc.allowHTTP, tc.allowPrivate)
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
