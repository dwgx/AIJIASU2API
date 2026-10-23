package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// statusTestLoginReply is a perfectly valid login body: the regression is that
// it must not be decoded when the HTTP status says the request failed.
const statusTestLoginReply = `{"Login":{"AuthResult":"OK","LoginToken":"tok-1|user",` +
	`"UserName":"user","ConnectPassword":"cp-1"}}`

// closeTrackingBody records whether the client closed the response body.
type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

// roundTripFunc answers a request without a network listener.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func statusTestDevice(t *testing.T) Device {
	t.Helper()
	device := DefaultDevice()
	if err := device.EnsureIDs(); err != nil {
		t.Fatalf("EnsureIDs: %v", err)
	}
	return device
}

// TestPostRejectsNon2xx is the P1-1 regression: a 4xx/5xx response used to be
// decoded as a successful login, so a vendor/WAF error page carrying a Login
// object could mark a healthy account auth_failed. Every non-2xx status must
// be an error that names the status, and the body must still be closed.
func TestPostRejectsNon2xx(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			body := &closeTrackingBody{Reader: strings.NewReader(statusTestLoginReply)}
			client := NewClient([]string{"https://control.invalid"}, statusTestDevice(t))
			client.HTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: status,
					Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       body,
					Request:    req,
				}, nil
			})}

			result, err := client.Login(context.Background(), "user", "password")
			if err == nil {
				t.Fatalf("Login on HTTP %d = nil error, want a status error", status)
			}
			if result != nil {
				t.Fatalf("Login on HTTP %d returned a result: %+v", status, result)
			}
			if want := fmt.Sprintf("HTTP %d", status); !strings.Contains(err.Error(), want) {
				t.Fatalf("Login error = %q, want it to contain %q", err, want)
			}
			if !body.closed {
				t.Fatal("post did not close the non-2xx response body")
			}
			if client.LoginToken != "" {
				t.Fatalf("a non-2xx response stored LoginToken %q", client.LoginToken)
			}
		})
	}
}

// TestPostAccepts2xx pins the other side of the boundary: a 2xx body still
// decodes normally.
func TestPostAccepts2xx(t *testing.T) {
	body := &closeTrackingBody{Reader: strings.NewReader(statusTestLoginReply)}
	client := NewClient([]string{"https://control.invalid"}, statusTestDevice(t))
	client.HTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       body,
			Request:    req,
		}, nil
	})}

	result, err := client.Login(context.Background(), "user", "password")
	if err != nil {
		t.Fatalf("Login on HTTP 200: %v", err)
	}
	if result == nil || !result.OK() {
		t.Fatalf("Login result = %+v, want an OK result", result)
	}
	if client.LoginToken != "tok-1|user" {
		t.Fatalf("LoginToken = %q, want tok-1|user", client.LoginToken)
	}
	if !body.closed {
		t.Fatal("post did not close the response body")
	}
}

// TestLoginSyncRejectsVendorErrorEnvelope is the other half of P1-1: a 200
// response whose Login object carries a non-OK AuthResult is a failed sync,
// not an account with zero servers.
func TestLoginSyncRejectsVendorErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Login":{"AuthResult":"ERR","AuthMessage":"system busy"}}`)
	}))
	t.Cleanup(srv.Close)

	client := NewClient([]string{srv.URL}, statusTestDevice(t))
	client.HTTP = srv.Client()
	client.LoginToken = "tok-1|user"
	client.UserName = "user"

	servers, err := client.LoginSync(context.Background())
	if err == nil {
		t.Fatalf("LoginSync on an ERR envelope = nil error, servers=%v", servers)
	}
	if servers != nil {
		t.Fatalf("LoginSync on an ERR envelope returned servers: %v", servers)
	}
	for _, want := range []string{"ERR", "system busy"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("LoginSync error = %q, want it to contain %q", err, want)
		}
	}
	if client.ConnectPassword != "" {
		t.Fatalf("LoginSync applied ConnectPassword %q from an error envelope", client.ConnectPassword)
	}
}

// TestLoginSyncAcceptsOKEnvelopeWithoutServers keeps the empty-list case
// separate from the error case: an OK envelope with no Servers field is a
// successful (empty) sync, which the daemon must not treat as a failure.
func TestLoginSyncAcceptsOKEnvelopeWithoutServers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Login":{"AuthResult":"OK","ConnectPassword":"cp-9"}}`)
	}))
	t.Cleanup(srv.Close)

	client := NewClient([]string{srv.URL}, statusTestDevice(t))
	client.HTTP = srv.Client()
	client.LoginToken = "tok-1|user"
	client.UserName = "user"

	servers, err := client.LoginSync(context.Background())
	if err != nil {
		t.Fatalf("LoginSync on an OK envelope: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("Servers = %v, want an empty map", servers)
	}
	if client.ConnectPassword != "cp-9" {
		t.Fatalf("ConnectPassword = %q, want the refreshed cp-9", client.ConnectPassword)
	}
}
