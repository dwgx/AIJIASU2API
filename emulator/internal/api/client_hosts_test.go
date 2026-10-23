package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// newHostStub starts a control API stub that answers every request with body.
func newHostStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newHostClient returns an unsigned Linux-shape client pointed at srv.
func newHostClient(t *testing.T, srv *httptest.Server, hosts ...string) *Client {
	t.Helper()
	device := Linux423Device()
	if err := device.EnsureLinuxIDs(); err != nil {
		t.Fatalf("EnsureLinuxIDs: %v", err)
	}
	client := NewClient(hosts, device)
	client.HTTP = srv.Client()
	client.NoSign = true
	return client
}

// TestHostListDecodesVendorShapes pins the two field shapes the vendor uses:
// a JSON array and one separator-delimited string.
func TestHostListDecodesVendorShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want HostList
	}{
		{
			name: "array",
			raw:  `["https://a.example","https://b.example"]`,
			want: HostList{"https://a.example", "https://b.example"},
		},
		{
			name: "newline string",
			raw:  `"https://a.example\nhttps://b.example"`,
			want: HostList{"https://a.example", "https://b.example"},
		},
		{
			name: "comma string",
			raw:  `"https://a.example, https://b.example"`,
			want: HostList{"https://a.example", "https://b.example"},
		},
		{
			name: "crlf and empty entries",
			raw:  `"https://a.example\r\n\nhttps://b.example,"`,
			want: HostList{"https://a.example", "https://b.example"},
		},
		{name: "null", raw: `null`, want: nil},
		{name: "unexpected object ignored", raw: `{"a":1}`, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got HostList
			if err := json.Unmarshal([]byte(tc.raw), &got); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("HostList = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLoginAppliesPushedHosts proves a successful login merges both pushed
// fields, keeps the configured host first, dedupes with a trailing slash and
// ignores cleartext entries.
func TestLoginAppliesPushedHosts(t *testing.T) {
	body := `{"Login":{"AuthResult":"OK","LoginToken":"tok|user","UserName":"u","ConnectPassword":"cp-1"},` +
		`"ApiUrls":"https://pushed-one.example/\nhttps://pushed-two.example",` +
		`"FetchedUrlHosts":["https://pushed-two.example/","http://cleartext.example","https://pushed-three.example"]}`
	srv := newHostStub(t, body)
	client := newHostClient(t, srv, srv.URL)

	if _, err := client.Login(context.Background(), "user", "pw"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	want := []string{
		srv.URL,
		"https://pushed-one.example",
		"https://pushed-two.example",
		"https://pushed-three.example",
	}
	if got := client.EffectiveHosts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("EffectiveHosts = %v, want %v", got, want)
	}

	// A second successful login must not duplicate the pushed entries.
	if _, err := client.Login(context.Background(), "user", "pw"); err != nil {
		t.Fatalf("second Login: %v", err)
	}
	if got := client.EffectiveHosts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("EffectiveHosts after repeat = %v, want %v", got, want)
	}

	// EffectiveHosts is a copy: the caller cannot rewrite the client's list.
	view := client.EffectiveHosts()
	view[0] = "https://mutated.example"
	if client.Hosts[0] != srv.URL {
		t.Fatalf("mutating EffectiveHosts changed Hosts[0] to %q", client.Hosts[0])
	}
}

// TestLoginSyncAppliesPushedHosts proves the sync response can extend the host
// list and that a configured duplicate keeps its original position.
func TestLoginSyncAppliesPushedHosts(t *testing.T) {
	body := `{"Login":{"AuthResult":"OK","ConnectPassword":"cp-2"},"Servers":{},` +
		`"ApiUrls":"https://pushed-one.example,https://pushed-two.example"}`
	srv := newHostStub(t, body)
	client := newHostClient(t, srv, srv.URL, "https://pushed-one.example/")
	client.LoginToken = "tok|user"

	if _, err := client.LoginSync(context.Background()); err != nil {
		t.Fatalf("LoginSync: %v", err)
	}
	want := []string{srv.URL, "https://pushed-one.example/", "https://pushed-two.example"}
	if got := client.EffectiveHosts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("EffectiveHosts = %v, want %v", got, want)
	}
}

// TestPushedHostsIgnoredWhenLoginFails keeps a rejected login from changing
// the host list.
func TestPushedHostsIgnoredWhenLoginFails(t *testing.T) {
	body := `{"Login":{"AuthResult":"ERR","AuthMessage":"bad"},` +
		`"ApiUrls":"https://pushed.example"}`
	srv := newHostStub(t, body)
	client := newHostClient(t, srv, srv.URL)

	result, err := client.Login(context.Background(), "user", "pw")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.OK() {
		t.Fatalf("LoginResult = %+v, want a rejection", result)
	}
	if got := client.EffectiveHosts(); !reflect.DeepEqual(got, []string{srv.URL}) {
		t.Fatalf("EffectiveHosts = %v, want only the configured host", got)
	}
}

// TestPushedHostsIgnoredWhenSyncFails keeps a vendor error envelope from
// changing the host list.
func TestPushedHostsIgnoredWhenSyncFails(t *testing.T) {
	body := `{"Login":{"AuthResult":"ERR","AuthMessage":"bad"},` +
		`"ApiUrls":"https://pushed.example"}`
	srv := newHostStub(t, body)
	client := newHostClient(t, srv, srv.URL)
	client.LoginToken = "tok|user"

	if _, err := client.LoginSync(context.Background()); err == nil {
		t.Fatal("LoginSync on an ERR envelope returned no error")
	}
	if got := client.EffectiveHosts(); !reflect.DeepEqual(got, []string{srv.URL}) {
		t.Fatalf("EffectiveHosts = %v, want only the configured host", got)
	}
}
