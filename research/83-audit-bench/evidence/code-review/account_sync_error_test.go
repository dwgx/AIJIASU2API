package account

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"ajsaccountd/internal/store"
)

// TestOverlayVendorSyncErrorWipesServers proves that a vendor loginSync error
// envelope (HTTP 200, AuthResult != OK, no Servers) is treated as a successful
// sync with zero servers and replaces the retained list.
func TestOverlayVendorSyncErrorWipesServers(t *testing.T) {
	st := store.NewWithSealer(plainSealer{})
	acct := newLoginAccount(t, st)
	var fail atomic.Bool
	server, _ := newFake(t, func(request capturedRequest) (int, string) {
		if request.form.Get("_client_login") == "" {
			return http.StatusOK, loginReply
		}
		if fail.Load() {
			return http.StatusOK, `{"Login":{"AuthResult":"ERR","AuthMessage":"system busy"}}`
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
	updated, err := service.Login(context.Background(), acct.ID)
	t.Logf("second Login: err=%v state=%q", err, updated.State)
	second, _ := service.Result(acct.ID)
	t.Logf("after vendor ERR sync: servers=%d", len(second.Servers))
	if len(second.Servers) != 0 {
		t.Fatalf("expected the retained server list to be replaced, got %d", len(second.Servers))
	}
}
