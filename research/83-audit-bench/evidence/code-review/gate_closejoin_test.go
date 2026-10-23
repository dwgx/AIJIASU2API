package gate

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// stuckSession ignores ctx, modelling an FV flow handshake that only ends at
// its own deadline (20 s in the real carrier).
type stuckSession struct {
	inside  atomic.Int32
	release chan struct{}
}

func (s *stuckSession) OpenFlow(context.Context, string, int) (net.Conn, error) {
	s.inside.Add(1)
	defer s.inside.Add(-1)
	<-s.release
	return nil, errors.New("released")
}

// TestOverlayCloseDoesNotJoinHandlers proves Close returns while a handler is
// still inside Session.OpenFlow.
func TestOverlayCloseDoesNotJoinHandlers(t *testing.T) {
	sess := &stuckSession{release: make(chan struct{})}
	srv, err := NewServer(Config{NodeID: "overlay", Limit: 2, DrainGrace: 50 * time.Millisecond}, sess)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	c := dialGate(t, srv)
	if method := greet(t, c, authNone); method != authNone {
		t.Fatalf("method = %#x", method)
	}
	connectRequest(t, c, cmdConnect, atypIPv4, []byte{203, 0, 113, 9}, 443)
	waitState(t, srv, func(st State) bool { return st.Active == 1 })

	done := make(chan struct{})
	go func() { _ = srv.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return")
	}
	t.Logf("Close returned with handlers still inside OpenFlow: %d", sess.inside.Load())
	if got := sess.inside.Load(); got != 1 {
		t.Fatalf("handler count inside OpenFlow after Close = %d, want 1", got)
	}
	close(sess.release)
	srv.handlers.Wait()
}
