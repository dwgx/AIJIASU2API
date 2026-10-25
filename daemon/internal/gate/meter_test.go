package gate

import (
	"bytes"
	"sync/atomic"
	"testing"
)

// meterRecorder is a fake progress callback. It records every chunk it is
// handed, refuses the call whose 1-based index is refuseAt, and accumulates the
// bytes it accepted so a test can pin exactly-once accounting.
type meterRecorder struct {
	calls    []int64
	accepted int64
	refuseAt int
}

func (r *meterRecorder) progress(n int64) bool {
	r.calls = append(r.calls, n)
	if len(r.calls) == r.refuseAt {
		return false
	}
	r.accepted += n
	return true
}

// reported is the sum of every chunk handed to the callback, refused or not.
func (r *meterRecorder) reported() int64 {
	var sum int64
	for _, n := range r.calls {
		sum += n
	}
	return sum
}

// TestMeteredWriterRefusedChunkChargedOnce is finding F1 from ACCEPTANCE-DRILL2:
// a chunk that crosses the budget was written to the tunnel once, so the meter
// must see it once. Before the fix Write returned errQuotaStopped with pending
// still set; copyFlow then called flush, which reported the same chunk again
// and advanced the durable counter twice.
func TestMeteredWriterRefusedChunkChargedOnce(t *testing.T) {
	var dst bytes.Buffer
	var counter atomic.Uint64
	rec := &meterRecorder{refuseAt: 2}
	m := &meteredWriter{w: &dst, n: &counter, progress: rec.progress}

	chunk := make([]byte, ProgressChunk)
	if _, err := m.Write(chunk); err != nil {
		t.Fatalf("first chunk error = %v, want nil", err)
	}
	if n, err := m.Write(chunk); n != len(chunk) || err != errQuotaStopped {
		t.Fatalf("second chunk = %d/%v, want %d/%v", n, err, len(chunk), errQuotaStopped)
	}
	if !m.flush() {
		t.Fatal("flush after refusal = false, want true")
	}

	written := int64(2 * ProgressChunk)
	refusedChunk := int64(ProgressChunk)
	if len(rec.calls) != 2 {
		t.Fatalf("progress calls = %v, want 2: the refused chunk must not be reported again", rec.calls)
	}
	if got := rec.reported(); got != written {
		t.Fatalf("reported bytes = %d, want %d (every relayed byte exactly once)", got, written)
	}
	if got := rec.accepted; got != written-refusedChunk {
		t.Fatalf("charged bytes = %d, want %d (bytes written minus the refused chunk)", got, written-refusedChunk)
	}
	if got := counter.Load(); got != uint64(written) {
		t.Fatalf("counter = %d, want %d", got, written)
	}
	if !m.refused {
		t.Fatal("writer did not record the refusal")
	}
}

// TestMeteredWriterFlushRefusalChargedOnce pins the trailing remainder path:
// a refusal raised by flush reports the remainder once, and a second flush
// (copyFlow calls exactly one, but the guard must hold) reports nothing.
func TestMeteredWriterFlushRefusalChargedOnce(t *testing.T) {
	var dst bytes.Buffer
	var counter atomic.Uint64
	rec := &meterRecorder{refuseAt: 1}
	m := &meteredWriter{w: &dst, n: &counter, progress: rec.progress}

	tail := []byte("tail")
	if _, err := m.Write(tail); err != nil {
		t.Fatalf("tail write error = %v, want nil", err)
	}
	if !m.flush() {
		t.Fatal("flush = false, want the refusal")
	}
	if !m.flush() {
		t.Fatal("second flush = false, want the recorded refusal")
	}
	if len(rec.calls) != 1 || rec.calls[0] != int64(len(tail)) {
		t.Fatalf("progress calls = %v, want one report of %d bytes", rec.calls, len(tail))
	}
	if got := counter.Load(); got != uint64(len(tail)) {
		t.Fatalf("counter = %d, want %d", got, len(tail))
	}
}

// TestMeteredWriterReportsChunksAndRemainderOnce pins the accepted path: full
// chunks are reported as they accumulate, flush reports only the remainder,
// and the sum equals the bytes written.
func TestMeteredWriterReportsChunksAndRemainderOnce(t *testing.T) {
	var dst bytes.Buffer
	var counter atomic.Uint64
	rec := &meterRecorder{}
	m := &meteredWriter{w: &dst, n: &counter, progress: rec.progress}

	chunk := make([]byte, ProgressChunk)
	for i := range 2 {
		if _, err := m.Write(chunk); err != nil {
			t.Fatalf("chunk %d error = %v, want nil", i+1, err)
		}
	}
	tail := []byte("remainder")
	if _, err := m.Write(tail); err != nil {
		t.Fatalf("tail write error = %v, want nil", err)
	}
	if m.flush() {
		t.Fatal("flush = true, want no refusal below the budget")
	}

	written := int64(2*ProgressChunk + len(tail))
	wantCalls := []int64{ProgressChunk, ProgressChunk, int64(len(tail))}
	if len(rec.calls) != len(wantCalls) {
		t.Fatalf("progress calls = %v, want %v", rec.calls, wantCalls)
	}
	for i := range wantCalls {
		if rec.calls[i] != wantCalls[i] {
			t.Fatalf("progress calls = %v, want %v", rec.calls, wantCalls)
		}
	}
	if got := rec.reported(); got != written {
		t.Fatalf("reported bytes = %d, want %d", got, written)
	}
	if got := rec.accepted; got != written {
		t.Fatalf("charged bytes = %d, want %d", got, written)
	}
	if got := counter.Load(); got != uint64(written) {
		t.Fatalf("counter = %d, want %d", got, written)
	}
}
