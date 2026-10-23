package account

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"ajsaccountd/internal/store"
)

// TestOverlayLoginSyncFailureDropsServers proves that a failed loginSync
// replaces the retained server list with an empty one.
func TestOverlayLoginSyncFailureDropsServers(t *testing.T) {
	st := store.NewWithSealer(plainSealer{})
	acct := newLoginAccount(t, st)
	var fail atomic.Bool
	server, _ := newFake(t, func(request capturedRequest) (int, string) {
		if request.form.Get("_client_login") == "" {
			return http.StatusOK, loginReply
		}
		if fail.Load() {
			return http.StatusBadGateway, `{"error":"upstream unavailable"}`
		}
		return http.StatusOK, syncReply
	})
	service := newTestLoginService(t, st, server.URL, nil)

	if _, err := service.Login(context.Background(), acct.ID); err != nil {
		t.Fatalf("first Login: %v", err)
	}
	first, _ := service.Result(acct.ID)
	t.Logf("after good sync: servers=%d", len(first.Servers))

	fail.Store(true)
	if _, err := service.Login(context.Background(), acct.ID); err != nil {
		t.Fatalf("second Login: %v", err)
	}
	second, _ := service.Result(acct.ID)
	t.Logf("after 502 sync: servers=%d nil=%v connectPassword=%q", len(second.Servers), second.Servers == nil, second.ConnectPassword.Reveal())
	if len(second.Servers) != 0 {
		t.Fatalf("expected the retained server list to be replaced by an empty one, got %d", len(second.Servers))
	}
}

// TestOverlayPostIgnoresHTTPStatus proves post returns a 5xx body as success.
func TestOverlayPostIgnoresHTTPStatus(t *testing.T) {
	server, _ := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusInternalServerError, `{"Login":{"AuthResult":"OK","LoginToken":"tok-9|u"}}`
	})
	client := NewControl([]string{server.URL}, mustDevice(t), Options{})
	result, err := client.Login(context.Background(), "user@example.com", Secret("pw"))
	t.Logf("control.Login on HTTP 500: err=%v result=%+v", err, result)
	if err == nil {
		t.Fatalf("HTTP 500 with a JSON body was treated as a successful login")
	}
}
