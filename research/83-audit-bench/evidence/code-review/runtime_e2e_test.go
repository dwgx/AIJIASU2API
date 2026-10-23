package runtime_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/store"
)

type overlaySealer struct{}

func (overlaySealer) Seal(_ string, plaintext []byte) ([]byte, error) {
	return append([]byte(nil), plaintext...), nil
}

func (overlaySealer) Open(_ string, sealed []byte) ([]byte, error) {
	return append([]byte(nil), sealed...), nil
}

const overlayLoginReply = `{"Login":{"AuthResult":"OK","LoginToken":"tok-1|user","UserName":"user","ConnectPassword":"cp-1","MembershipStatus":"vip"}}`
const overlaySyncReply = `{"Login":{"ConnectPassword":"cp-2"},"Servers":{"s1":{"id":"s1","vsid":1,"svr":"203.0.113.10","ctry":"CN","sts":"ok","ord":"1","par":{"fv":{"p":443}},"loc":[{"c":"CN","n":"Shanghai"}]}}}`

// TestOverlayLoginSyncErrorWipesNodes proves that a vendor loginSync error
// envelope deletes every node and gate of the account on the next sync.
func TestOverlayLoginSyncErrorWipesNodes(t *testing.T) {
	requirePort13000(t)
	st := store.NewWithSealer(overlaySealer{})
	if err := st.Restore([]byte(`{"version":1}`)); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	acct := newNodeTestAccount(t, st, "overlay-wipe")

	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.PostForm.Get("_client_login") == "" {
			_, _ = io.WriteString(w, overlayLoginReply)
			return
		}
		if fail.Load() {
			_, _ = io.WriteString(w, `{"Login":{"AuthResult":"ERR","AuthMessage":"system busy"}}`)
			return
		}
		_, _ = io.WriteString(w, overlaySyncReply)
	}))
	t.Cleanup(srv.Close)

	service, err := account.NewLoginService(st, account.LoginConfig{
		Hosts:   []string{srv.URL},
		Enabled: true,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewLoginService: %v", err)
	}
	t.Cleanup(service.Close)

	opener := newFakeSessionOpener()
	rec := &fakeReconciler{}
	controller := newNodeController(t, st, service, opener, rec)

	ctx := context.Background()
	if _, err := service.Login(ctx, acct.ID); err != nil {
		t.Fatalf("first Login: %v", err)
	}
	if err := controller.SyncAccount(ctx, acct.ID); err != nil {
		t.Fatalf("first SyncAccount: %v", err)
	}
	t.Logf("after good login+sync: nodes=%d", len(st.Nodes()))

	fail.Store(true)
	if _, err := service.Login(ctx, acct.ID); err != nil {
		t.Fatalf("second Login: %v", err)
	}
	syncErr := controller.SyncAccount(ctx, acct.ID)
	nodes := st.Nodes()
	t.Logf("after vendor ERR loginSync: syncErr=%v nodes=%d", syncErr, len(nodes))
	if len(nodes) != 0 {
		t.Fatalf("expected every node to be deleted, got %d", len(nodes))
	}
	if session := opener.session("s1"); session != nil && !session.isClosed() {
		t.Fatal("gate session survived the wipe")
	}
}
