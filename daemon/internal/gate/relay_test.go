package gate

import (
	"bytes"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

// halfCloseRecorder is a net.Conn sink that records which shutdown the relay
// chose. Only Write, CloseWrite and Close are called by copyFlow.
type halfCloseRecorder struct {
	net.Conn

	mu          sync.Mutex
	buf         bytes.Buffer
	closeWrites int
	closes      int
}

func (r *halfCloseRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *halfCloseRecorder) CloseWrite() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeWrites++
	return nil
}

func (r *halfCloseRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closes++
	return nil
}

// TestCopyFlowHalfCloses checks that the relay half-closes a destination that
// supports CloseWrite instead of closing it, so the reverse direction can
// still carry the response.
func TestCopyFlowHalfCloses(t *testing.T) {
	src, peer := net.Pipe()
	defer peer.Close()
	payload := []byte("GET / HTTP/1.0\r\n\r\n")
	go func() {
		_, _ = peer.Write(payload)
		_ = peer.Close()
	}()

	dst := &halfCloseRecorder{}
	var counter atomic.Uint64
	n := copyFlow(dst, src, &counter, nil, func() {})
	if n != int64(len(payload)) {
		t.Fatalf("copied %d bytes, want %d", n, len(payload))
	}
	if got := counter.Load(); got != uint64(len(payload)) {
		t.Fatalf("counter = %d, want %d", got, len(payload))
	}
	if got := dst.buf.String(); got != string(payload) {
		t.Fatalf("dst payload = %q, want %q", got, payload)
	}
	if dst.closeWrites != 1 {
		t.Fatalf("CloseWrite calls = %d, want 1", dst.closeWrites)
	}
	if dst.closes != 0 {
		t.Fatalf("Close calls = %d, want 0 (half-close must keep the response direction)", dst.closes)
	}
}
