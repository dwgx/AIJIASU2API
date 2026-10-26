package store_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ajsaccountd/internal/store"
)

// journalTestHeader is the fixed log header: magic (9) + format (1) +
// generation (16) + big-endian startSeq (8).
const journalTestHeader = 34

var journalTestCRC = crc32.MakeTable(crc32.Castagnoli)

func openTestJournal(t *testing.T, dir string) *store.Journal {
	t.Helper()
	j, err := store.OpenJournal(dir)
	if err != nil {
		t.Fatalf("OpenJournal(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

// initTestJournal opens a fresh directory and writes generation seq 0 from an
// empty store, which is the same one-shot import path a legacy state.json uses.
func initTestJournal(t *testing.T, dir string) (*store.Journal, *store.Store) {
	t.Helper()
	j := openTestJournal(t, dir)
	st := store.NewEmpty()
	report, err := j.Recover(st)
	if err != nil {
		t.Fatalf("Recover fresh journal: %v", err)
	}
	if !report.Fresh {
		t.Fatal("fresh journal recovery did not report a fresh generation")
	}
	if !j.Initialized() {
		t.Fatal("journal is not initialized after Recover")
	}
	if j.Seq() != 0 {
		t.Fatalf("fresh journal seq = %d, want 0", j.Seq())
	}
	return j, st
}

func mustSnapshot(t *testing.T, st *store.Store) []byte {
	t.Helper()
	data, err := st.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return data
}

// appendAccount mutates the store and commits one whole frame. It returns the
// committed sequence and the exact snapshot bytes.
func appendAccount(t *testing.T, j *store.Journal, st *store.Store, label string) (uint64, []byte) {
	t.Helper()
	if _, err := st.CreateAccount(accountInput(label, persistPassword)); err != nil {
		t.Fatalf("CreateAccount(%s): %v", label, err)
	}
	data := mustSnapshot(t, st)
	seq, err := j.Append(data)
	if err != nil {
		t.Fatalf("Append(%s): %v", label, err)
	}
	return seq, data
}

func readJournalLog(t *testing.T, dir string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "state.log"))
	if err != nil {
		t.Fatalf("read state.log: %v", err)
	}
	return data
}

func writeJournalLog(t *testing.T, dir string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "state.log"), data, 0o600); err != nil {
		t.Fatalf("write state.log: %v", err)
	}
}

func readJournalCurrent(t *testing.T, dir string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "CURRENT"))
	if err != nil {
		t.Fatalf("read CURRENT: %v", err)
	}
	return data
}

func writeJournalCurrent(t *testing.T, dir string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "CURRENT"), data, 0o600); err != nil {
		t.Fatalf("write CURRENT: %v", err)
	}
}

type journalTestFrame struct {
	start int
	body  int
	size  int
}

func journalTestFrames(t *testing.T, log []byte) []journalTestFrame {
	t.Helper()
	if len(log) < journalTestHeader {
		t.Fatalf("log is %d bytes, want at least %d", len(log), journalTestHeader)
	}
	var frames []journalTestFrame
	offset := journalTestHeader
	for offset+8 <= len(log) {
		length := int(binary.BigEndian.Uint32(log[offset:]))
		if length <= 0 || offset+8+length > len(log) {
			break
		}
		frames = append(frames, journalTestFrame{start: offset, body: offset + 8, size: length})
		offset += 8 + length
	}
	return frames
}

type journalTestBody struct {
	Format     int             `json:"format"`
	Generation string          `json:"generation"`
	Seq        uint64          `json:"seq"`
	Snapshot   json.RawMessage `json:"snapshot"`
}

// rewriteJournalFrame re-encodes one frame body and repairs its length and
// CRC32C, so only the intended semantic check can reject it.
func rewriteJournalFrame(t *testing.T, log []byte, index int, mutate func(*journalTestBody)) []byte {
	t.Helper()
	frames := journalTestFrames(t, log)
	if index < 0 || index >= len(frames) {
		t.Fatalf("frame %d missing (%d frames)", index, len(frames))
	}
	frame := frames[index]
	var body journalTestBody
	if err := json.Unmarshal(log[frame.body:frame.body+frame.size], &body); err != nil {
		t.Fatalf("decode frame %d: %v", index, err)
	}
	mutate(&body)
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode frame %d: %v", index, err)
	}
	replacement := make([]byte, 8+len(encoded))
	binary.BigEndian.PutUint32(replacement[:4], uint32(len(encoded)))
	binary.BigEndian.PutUint32(replacement[4:8], crc32.Checksum(encoded, journalTestCRC))
	copy(replacement[8:], encoded)
	out := append([]byte(nil), log[:frame.start]...)
	out = append(out, replacement...)
	out = append(out, log[frame.start+8+frame.size:]...)
	return out
}

func corruptJournalFrame(t *testing.T, log []byte, index int) []byte {
	t.Helper()
	frames := journalTestFrames(t, log)
	if index < 0 || index >= len(frames) {
		t.Fatalf("frame %d missing (%d frames)", index, len(frames))
	}
	out := append([]byte(nil), log...)
	out[frames[index].body+1] ^= 0xff
	return out
}

func TestJournalAppendReplayRoundtrip(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	generation := j.Generation()
	if len(generation) != 32 {
		t.Fatalf("generation = %q, want 32 hex characters", generation)
	}

	if seq, _ := appendAccount(t, j, st, "journal-one"); seq != 1 {
		t.Fatalf("first append seq = %d, want 1", seq)
	}
	seq, want := appendAccount(t, j, st, "journal-two")
	if seq != 2 {
		t.Fatalf("second append seq = %d, want 2", seq)
	}
	if j.Seq() != 2 {
		t.Fatalf("journal seq = %d, want 2", j.Seq())
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := store.OpenJournal(dir)
	if err != nil {
		t.Fatalf("reopen journal: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if reopened.Generation() != generation {
		t.Fatalf("generation changed: %q -> %q", generation, reopened.Generation())
	}
	if reopened.Seq() != 2 {
		t.Fatalf("reopened seq = %d, want 2", reopened.Seq())
	}
	if !reopened.Initialized() {
		t.Fatal("reopened journal reports a fresh directory")
	}

	replayed := store.NewEmpty()
	report, err := reopened.Recover(replayed)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.Fresh {
		t.Fatal("recovery of an initialized journal reported fresh")
	}
	if report.Frames != 2 || report.LastSeq != 2 || report.CheckpointSeq != 2 {
		t.Fatalf("report = %+v, want 2 frames replayed and checkpointed at seq 2", report)
	}
	if report.Generation != generation {
		t.Fatalf("report generation = %q, want %q", report.Generation, generation)
	}
	if got := mustSnapshot(t, replayed); !bytes.Equal(got, want) {
		t.Fatal("replayed snapshot differs from the last committed snapshot")
	}

	// Recovery compacted the replayed log, so the next commit continues at 3.
	next, err := reopened.Append(mustSnapshot(t, replayed))
	if err != nil {
		t.Fatalf("append after recovery: %v", err)
	}
	if next != 3 {
		t.Fatalf("append after recovery seq = %d, want 3", next)
	}
}

func TestJournalDiscardsIncompleteFinalFrame(t *testing.T) {
	t.Run("torn body", func(t *testing.T) {
		dir := t.TempDir()
		j, st := initTestJournal(t, dir)
		_, first := appendAccount(t, j, st, "torn-one")
		appendAccount(t, j, st, "torn-two")

		log := readJournalLog(t, dir)
		frames := journalTestFrames(t, log)
		if len(frames) != 2 {
			t.Fatalf("log has %d frames, want 2", len(frames))
		}
		last := frames[1]
		writeJournalLog(t, dir, append([]byte(nil), log[:last.body+last.size/2]...))
		if err := j.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		reopened, err := store.OpenJournal(dir)
		if err != nil {
			t.Fatalf("OpenJournal after a torn final frame: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		replayed := store.NewEmpty()
		report, err := reopened.Recover(replayed)
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if report.Frames != 1 || report.LastSeq != 1 {
			t.Fatalf("report = %+v, want only the first frame replayed", report)
		}
		if got := mustSnapshot(t, replayed); !bytes.Equal(got, first) {
			t.Fatal("replayed state is not the first committed frame")
		}
	})

	t.Run("partial next header", func(t *testing.T) {
		dir := t.TempDir()
		j, st := initTestJournal(t, dir)
		appendAccount(t, j, st, "partial-one")
		appendAccount(t, j, st, "partial-two")
		log := readJournalLog(t, dir)
		writeJournalLog(t, dir, append(append([]byte(nil), log...), 0x00, 0x00, 0x01))
		if err := j.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		reopened, err := store.OpenJournal(dir)
		if err != nil {
			t.Fatalf("OpenJournal after a partial frame header: %v", err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		report, err := reopened.Recover(store.NewEmpty())
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if report.Frames != 2 || report.LastSeq != 2 {
			t.Fatalf("report = %+v, want both complete frames replayed", report)
		}
	})
}

func TestJournalRejectsCorruptFrame(t *testing.T) {
	for name, index := range map[string]int{"interior": 0, "final": 1} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			j, st := initTestJournal(t, dir)
			appendAccount(t, j, st, "crc-one")
			appendAccount(t, j, st, "crc-two")
			writeJournalLog(t, dir, corruptJournalFrame(t, readJournalLog(t, dir), index))
			if err := j.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if _, err := store.OpenJournal(dir); err == nil {
				t.Fatal("corrupt frame was accepted")
			} else if !strings.Contains(err.Error(), "CRC32C") {
				t.Fatalf("error = %v, want a CRC32C failure", err)
			}
		})
	}
}

func TestJournalRejectsBadFrames(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, []byte) []byte
		want   string
	}{
		{
			name: "skipped seq",
			mutate: func(t *testing.T, log []byte) []byte {
				return rewriteJournalFrame(t, log, 1, func(body *journalTestBody) { body.Seq = 3 })
			},
			want: "seq",
		},
		{
			name: "duplicate seq",
			mutate: func(t *testing.T, log []byte) []byte {
				return rewriteJournalFrame(t, log, 1, func(body *journalTestBody) { body.Seq = 1 })
			},
			want: "seq",
		},
		{
			name: "body generation",
			mutate: func(t *testing.T, log []byte) []byte {
				return rewriteJournalFrame(t, log, 1, func(body *journalTestBody) {
					body.Generation = strings.Repeat("0", 32)
				})
			},
			want: "generation",
		},
		{
			name: "frame format",
			mutate: func(t *testing.T, log []byte) []byte {
				return rewriteJournalFrame(t, log, 1, func(body *journalTestBody) { body.Format = 2 })
			},
			want: "format",
		},
		{
			name: "header generation",
			mutate: func(t *testing.T, log []byte) []byte {
				out := append([]byte(nil), log...)
				out[10] ^= 0xff
				return out
			},
			want: "generation",
		},
		{
			name: "oversize frame",
			mutate: func(t *testing.T, log []byte) []byte {
				return append(append([]byte(nil), log...), 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0)
			},
			want: "body length",
		},
		{
			name: "zero length frame",
			mutate: func(t *testing.T, log []byte) []byte {
				return append(append([]byte(nil), log...), 0, 0, 0, 0, 0, 0, 0, 0)
			},
			want: "body length",
		},
		{
			name: "truncated header",
			mutate: func(t *testing.T, log []byte) []byte {
				return append([]byte(nil), log[:20]...)
			},
			want: "complete header",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			j, st := initTestJournal(t, dir)
			appendAccount(t, j, st, "bad-one")
			appendAccount(t, j, st, "bad-two")
			writeJournalLog(t, dir, tc.mutate(t, readJournalLog(t, dir)))
			if err := j.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if _, err := store.OpenJournal(dir); err == nil {
				t.Fatal("corrupt journal was accepted")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestJournalCheckpointCompacts(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	appendAccount(t, j, st, "checkpoint-one")
	appendAccount(t, j, st, "checkpoint-two")
	snapshot := mustSnapshot(t, st)
	generation := j.Generation()

	seq, err := j.Checkpoint(snapshot)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if seq != 2 || j.Seq() != 2 {
		t.Fatalf("checkpoint seq = %d (journal %d), want 2", seq, j.Seq())
	}

	var pointer struct {
		Version    int    `json:"version"`
		Generation string `json:"generation"`
		Seq        uint64 `json:"seq"`
		SHA256     string `json:"sha256"`
	}
	if err := json.Unmarshal(readJournalCurrent(t, dir), &pointer); err != nil {
		t.Fatalf("decode CURRENT: %v", err)
	}
	sum := sha256.Sum256(snapshot)
	if pointer.Version != 1 || pointer.Generation != generation || pointer.Seq != 2 {
		t.Fatalf("CURRENT = %+v, want version 1 generation %s seq 2", pointer, generation)
	}
	if pointer.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("CURRENT sha256 = %q, want the checkpoint digest", pointer.SHA256)
	}

	checkpointPath := filepath.Join(dir, "checkpoints", fmt.Sprintf("%s-2.json", generation))
	checkpoint, err := os.ReadFile(checkpointPath)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	if !bytes.Equal(checkpoint, snapshot) {
		t.Fatal("checkpoint bytes differ from the committed snapshot")
	}

	archives, err := filepath.Glob(filepath.Join(dir, "state.log.*"))
	if err != nil {
		t.Fatalf("glob archives: %v", err)
	}
	if len(archives) != 1 {
		t.Fatalf("archives = %v, want exactly one archived log", archives)
	}
	log := readJournalLog(t, dir)
	if start := binary.BigEndian.Uint64(log[26:34]); start != 3 {
		t.Fatalf("new log start seq = %d, want 3", start)
	}
	if frames := journalTestFrames(t, log); len(frames) != 0 {
		t.Fatalf("new log has %d frames, want 0", len(frames))
	}

	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := store.OpenJournal(dir)
	if err != nil {
		t.Fatalf("reopen after checkpoint: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if reopened.Seq() != 2 {
		t.Fatalf("reopened seq = %d, want 2", reopened.Seq())
	}
	replayed := store.NewEmpty()
	report, err := reopened.Recover(replayed)
	if err != nil {
		t.Fatalf("Recover checkpoint: %v", err)
	}
	if report.Frames != 0 || report.LastSeq != 2 || report.CheckpointSeq != 2 {
		t.Fatalf("report = %+v, want a checkpoint-only recovery at seq 2", report)
	}
	if got := mustSnapshot(t, replayed); !bytes.Equal(got, snapshot) {
		t.Fatal("checkpoint replay differs from the checkpointed snapshot")
	}
	next, err := reopened.Append(mustSnapshot(t, replayed))
	if err != nil {
		t.Fatalf("append after checkpoint recovery: %v", err)
	}
	if next != 3 {
		t.Fatalf("append after checkpoint recovery seq = %d, want 3", next)
	}
}

func TestJournalRejectsCorruptCurrent(t *testing.T) {
	// setup creates an initialized journal with one checkpoint and returns its
	// directory, generation and checkpoint path.
	setup := func(t *testing.T) (string, string, string) {
		t.Helper()
		dir := t.TempDir()
		j, st := initTestJournal(t, dir)
		appendAccount(t, j, st, "current-one")
		if _, err := j.Checkpoint(mustSnapshot(t, st)); err != nil {
			t.Fatalf("Checkpoint: %v", err)
		}
		if err := j.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		generation := j.Generation()
		checkpointPath := filepath.Join(dir, "checkpoints", fmt.Sprintf("%s-1.json", generation))
		return dir, generation, checkpointPath
	}

	t.Run("empty", func(t *testing.T) {
		dir, _, _ := setup(t)
		writeJournalCurrent(t, dir, nil)
		if _, err := store.OpenJournal(dir); err == nil {
			t.Fatal("empty CURRENT was accepted")
		}
	})

	t.Run("garbage", func(t *testing.T) {
		dir, _, _ := setup(t)
		writeJournalCurrent(t, dir, []byte("{not json"))
		if _, err := store.OpenJournal(dir); err == nil {
			t.Fatal("garbage CURRENT was accepted")
		}
	})

	t.Run("wrong version", func(t *testing.T) {
		dir, _, _ := setup(t)
		var pointer map[string]any
		if err := json.Unmarshal(readJournalCurrent(t, dir), &pointer); err != nil {
			t.Fatalf("decode CURRENT: %v", err)
		}
		pointer["version"] = 2
		data, _ := json.Marshal(pointer)
		writeJournalCurrent(t, dir, data)
		if _, err := store.OpenJournal(dir); err == nil {
			t.Fatal("future CURRENT version was accepted")
		}
	})

	t.Run("sha mismatch", func(t *testing.T) {
		dir, _, _ := setup(t)
		var pointer map[string]any
		if err := json.Unmarshal(readJournalCurrent(t, dir), &pointer); err != nil {
			t.Fatalf("decode CURRENT: %v", err)
		}
		digest := pointer["sha256"].(string)
		replacement := "0" + digest[1:]
		if replacement == digest {
			replacement = "1" + digest[1:]
		}
		pointer["sha256"] = replacement
		data, _ := json.Marshal(pointer)
		writeJournalCurrent(t, dir, data)
		if _, err := store.OpenJournal(dir); err == nil {
			t.Fatal("CURRENT with a mismatched digest was accepted")
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		dir, _, _ := setup(t)
		var pointer map[string]any
		if err := json.Unmarshal(readJournalCurrent(t, dir), &pointer); err != nil {
			t.Fatalf("decode CURRENT: %v", err)
		}
		pointer["extra"] = 1
		data, _ := json.Marshal(pointer)
		writeJournalCurrent(t, dir, data)
		if _, err := store.OpenJournal(dir); err == nil {
			t.Fatal("CURRENT with an unknown field was accepted")
		}
	})

	t.Run("missing checkpoint", func(t *testing.T) {
		dir, _, checkpointPath := setup(t)
		if err := os.Remove(checkpointPath); err != nil {
			t.Fatalf("remove checkpoint: %v", err)
		}
		if _, err := store.OpenJournal(dir); err == nil {
			t.Fatal("CURRENT pointing at a missing checkpoint was accepted")
		}
	})

	t.Run("log without current", func(t *testing.T) {
		dir, _, _ := setup(t)
		if err := os.Remove(filepath.Join(dir, "CURRENT")); err != nil {
			t.Fatalf("remove CURRENT: %v", err)
		}
		if _, err := store.OpenJournal(dir); err == nil {
			t.Fatal("a log without CURRENT was accepted")
		} else if !strings.Contains(err.Error(), "committed journal files remain") {
			t.Fatalf("error = %v, want an ambiguity refusal", err)
		}
	})
}

func TestJournalRefusesFreshStartWithSurvivingArtifacts(t *testing.T) {
	// setup leaves one checkpoint, one archived log and a fresh active log.
	setup := func(t *testing.T) (string, string) {
		t.Helper()
		dir := t.TempDir()
		j, st := initTestJournal(t, dir)
		appendAccount(t, j, st, "artifact-one")
		if _, err := j.Checkpoint(mustSnapshot(t, st)); err != nil {
			t.Fatalf("Checkpoint: %v", err)
		}
		generation := j.Generation()
		if err := j.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		return dir, generation
	}

	t.Run("checkpoint survives", func(t *testing.T) {
		dir, generation := setup(t)
		if err := os.Remove(filepath.Join(dir, "CURRENT")); err != nil {
			t.Fatalf("remove CURRENT: %v", err)
		}
		if err := os.Remove(filepath.Join(dir, "state.log")); err != nil {
			t.Fatalf("remove state.log: %v", err)
		}
		archives, err := filepath.Glob(filepath.Join(dir, "state.log.*"))
		if err != nil {
			t.Fatalf("glob archives: %v", err)
		}
		for _, archive := range archives {
			if err := os.Remove(archive); err != nil {
				t.Fatalf("remove archive %s: %v", archive, err)
			}
		}
		// Only checkpoints/<generation>-1.json remains.
		if _, err := os.Stat(filepath.Join(dir, "checkpoints", generation+"-1.json")); err != nil {
			t.Fatalf("checkpoint missing from the fixture: %v", err)
		}
		if _, err := store.OpenJournal(dir); err == nil {
			t.Fatal("a surviving checkpoint without CURRENT was re-imported")
		} else if !strings.Contains(err.Error(), "committed journal files remain") {
			t.Fatalf("error = %v, want an ambiguity refusal", err)
		}
	})

	t.Run("archived log survives", func(t *testing.T) {
		dir, _ := setup(t)
		if err := os.Remove(filepath.Join(dir, "CURRENT")); err != nil {
			t.Fatalf("remove CURRENT: %v", err)
		}
		if err := os.Remove(filepath.Join(dir, "state.log")); err != nil {
			t.Fatalf("remove state.log: %v", err)
		}
		if err := os.RemoveAll(filepath.Join(dir, "checkpoints")); err != nil {
			t.Fatalf("remove checkpoints: %v", err)
		}
		archives, err := filepath.Glob(filepath.Join(dir, "state.log.*"))
		if err != nil || len(archives) == 0 {
			t.Fatalf("archives = %v, err = %v, want a surviving archive", archives, err)
		}
		if _, err := store.OpenJournal(dir); err == nil {
			t.Fatal("a surviving archived log without CURRENT was re-imported")
		} else if !strings.Contains(err.Error(), "committed journal files remain") {
			t.Fatalf("error = %v, want an ambiguity refusal", err)
		}
	})

	t.Run("clean directory is fresh", func(t *testing.T) {
		dir := t.TempDir()
		j, err := store.OpenJournal(dir)
		if err != nil {
			t.Fatalf("OpenJournal on a clean directory: %v", err)
		}
		defer func() { _ = j.Close() }()
		if j.Initialized() {
			t.Fatal("clean directory reported an initialized journal")
		}
	})

	t.Run("interrupted temp write is ignored", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "checkpoints"), 0o700); err != nil {
			t.Fatalf("create checkpoints: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "checkpoints", ".journal-crashed.tmp"), []byte("partial"), 0o600); err != nil {
			t.Fatalf("write temp: %v", err)
		}
		j, err := store.OpenJournal(dir)
		if err != nil {
			t.Fatalf("OpenJournal with only a temp checkpoint: %v", err)
		}
		defer func() { _ = j.Close() }()
		if j.Initialized() {
			t.Fatal("temp-only directory reported an initialized journal")
		}
	})
}

func TestJournalLockPreventsSecondOpener(t *testing.T) {
	dir := t.TempDir()
	first, err := store.OpenJournal(dir)
	if err != nil {
		t.Fatalf("first OpenJournal: %v", err)
	}
	second, err := store.OpenJournal(dir)
	if err == nil {
		_ = second.Close()
		_ = first.Close()
		t.Fatal("second OpenJournal acquired a locked directory")
	}
	if !errors.Is(err, store.ErrStateLocked) {
		t.Fatalf("second OpenJournal error = %v, want ErrStateLocked", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// A stale lock file is reusable: the OS lock, not the file, is ownership.
	third, err := store.OpenJournal(dir)
	if err != nil {
		t.Fatalf("OpenJournal after Close: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatalf("Close third: %v", err)
	}
}

func TestJournalRecoversWithoutActiveLog(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	appendAccount(t, j, st, "nolog-one")
	snapshot := mustSnapshot(t, st)
	if _, err := j.Checkpoint(snapshot); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Crash after archiving the old log and before creating its successor.
	if err := os.Remove(filepath.Join(dir, "state.log")); err != nil {
		t.Fatalf("remove state.log: %v", err)
	}

	reopened, err := store.OpenJournal(dir)
	if err != nil {
		t.Fatalf("OpenJournal without an active log: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if reopened.Seq() != 1 {
		t.Fatalf("seq = %d, want the checkpoint seq 1", reopened.Seq())
	}
	replayed := store.NewEmpty()
	report, err := reopened.Recover(replayed)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.Frames != 0 || report.LastSeq != 1 {
		t.Fatalf("report = %+v, want a checkpoint-only recovery", report)
	}
	if got := mustSnapshot(t, replayed); !bytes.Equal(got, snapshot) {
		t.Fatal("checkpoint replay differs from the checkpointed snapshot")
	}
}

func TestJournalRejectsInvalidSnapshotReplay(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	appendAccount(t, j, st, "replay-valid")

	// Structurally a v2 document, semantically invalid: two accounts share an
	// id. Append only checks the envelope, so the corruption reaches Recover.
	invalid := []byte(`{"version":2,"accounts":[{"id":"acc-1"},{"id":"acc-1"}]}`)
	if _, err := j.Append(invalid); err != nil {
		t.Fatalf("Append rejected a structurally valid v2 document: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := store.OpenJournal(dir)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	target := store.NewEmpty()
	if _, err := reopened.Recover(target); err == nil {
		t.Fatal("invalid snapshot was replayed")
	}
	if len(target.Accounts()) != 0 {
		t.Fatal("replay mutated the store before validation finished")
	}
}

func TestJournalRefusesSymlinkedLog(t *testing.T) {
	dir := t.TempDir()
	j, st := initTestJournal(t, dir)
	appendAccount(t, j, st, "symlink-one")
	if err := j.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	logPath := filepath.Join(dir, "state.log")
	moved := filepath.Join(dir, "state.log.real")
	if err := os.Rename(logPath, moved); err != nil {
		t.Fatalf("rename log: %v", err)
	}
	if err := os.Symlink(moved, logPath); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	if _, err := store.OpenJournal(dir); err == nil {
		t.Fatal("symlinked state.log was accepted")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v, want a symlink refusal", err)
	}
}
