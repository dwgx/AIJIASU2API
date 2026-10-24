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
	"ajsaccountd/internal/model"
	"ajsaccountd/internal/store"
)

// loginSyncSealer is an identity sealer: this test exercises the sync path,
// not encryption at rest, and never touches a real credential.
type loginSyncSealer struct{}

func (loginSyncSealer) Seal(_ string, plaintext []byte) ([]byte, error) {
	return append([]byte(nil), plaintext...), nil
}

func (loginSyncSealer) Open(_ string, sealed []byte) ([]byte, error) {
	return append([]byte(nil), sealed...), nil
}

// Delete satisfies store.Sealer: this stub keeps nothing, so there is nothing
// to remove.
func (loginSyncSealer) Delete(string) error { return nil }

const (
	retainedLoginReply = `{"Login":{"AuthResult":"OK","LoginToken":"tok-1|user",` +
		`"UserName":"user","ConnectPassword":"cp-1","MembershipStatus":"vip"}}`
	retainedSyncReply = `{"Login":{"ConnectPassword":"cp-2"},"Servers":{"s1":{"id":"s1",` +
		`"vsid":1,"svr":"203.0.113.10","ctry":"CN","sts":"ok","ord":"1",` +
		`"par":{"fv":{"p":443}},"loc":[{"c":"CN","n":"Shanghai"}]}}}`
	retainedVendorError = `{"Login":{"AuthResult":"ERR","AuthMessage":"system busy"}}`
)

// TestLoginSyncFailureKeepsNodes is the P1-2 end-to-end regression: a good
// login opens one node, then a second login whose loginSync returns a vendor
// error envelope must not wipe that node. Before the fix the empty server map
// became the retained result, SyncAccount's desired set was empty, and every
// gate/session/node of the account was removed while the login reported
// success.
func TestLoginSyncFailureKeepsNodes(t *testing.T) {
	requirePort13000(t)
	st := store.NewWithSealer(loginSyncSealer{})
	if err := st.Restore([]byte(`{"version":1}`)); err != nil {
		t.Fatalf("Restore empty snapshot: %v", err)
	}
	acct := newNodeTestAccount(t, st, "login-sync-keeps-nodes")

	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.PostForm.Get("_client_login") == "" {
			_, _ = io.WriteString(w, retainedLoginReply)
			return
		}
		if fail.Load() {
			_, _ = io.WriteString(w, retainedVendorError)
			return
		}
		_, _ = io.WriteString(w, retainedSyncReply)
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
	nodes := st.Nodes()
	if len(nodes) != 1 || nodes[0].State != model.NodeLive {
		t.Fatalf("after the good sync: nodes = %+v, want one LIVE node", nodes)
	}

	fail.Store(true)
	if _, err := service.Login(ctx, acct.ID); err != nil {
		t.Fatalf("second Login must still succeed: %v", err)
	}
	if err := controller.SyncAccount(ctx, acct.ID); err != nil {
		t.Fatalf("SyncAccount after the failed sync: %v", err)
	}
	nodes = st.Nodes()
	if len(nodes) != 1 {
		t.Fatalf("after the failed sync: nodes = %d, want the retained node", len(nodes))
	}
	if nodes[0].State != model.NodeLive || nodes[0].GatePort == 0 {
		t.Fatalf("retained node = %+v, want LIVE with its gate port", nodes[0])
	}
	if session := opener.session("s1"); session == nil || session.isClosed() {
		t.Fatal("the failed sync closed the retained node's session")
	}
}
