package store

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The state journal is the crash-safe commit log beside state.json
// (DESIGN F6, PersistenceDesign §2). One directory holds:
//
//	state.lock                              OS-advisory exclusive lock
//	CURRENT                                 committed checkpoint pointer
//	checkpoints/<generation>-<seq>.json     immutable v2 snapshot
//	state.log                               active append-only frame log
//	state.log.<generation>.<start>-<seq>    archived logs (retained)
//
// A frame is a 4-byte big-endian body length, a 4-byte big-endian CRC32C of
// the body, then the body. The body is canonical JSON carrying the format, the
// 128-bit generation, the frame sequence and the whole post-mutation v2
// snapshot. The log header repeats the magic, format, raw generation and the
// first sequence number, so a log can never be spliced onto another
// generation.
//
// This file implements the format, open/replay/append/checkpoint helpers and
// the directory lock. Store.commitLocked is the live writer: money, grant,
// lease, quota and audit mutations append their post-state through it, while
// the periodic Checkpoint compacts the log and refreshes the state.json export.
const (
	// journalFormatVersion is the only frame, body and pointer format this
	// build writes and accepts. It is repeated in the log header, every frame
	// and CURRENT so a truncated or spliced log fails closed.
	journalFormatVersion = 1
	journalMagic         = "AJSSTATE1"

	// journalHeaderBytes is magic (9) + format (1) + generation (16) +
	// big-endian startSeq (8).
	journalHeaderBytes int64 = int64(len(journalMagic) + 1 + 16 + 8)

	// journalMaxFrameBytes bounds one frame body. A snapshot that cannot fit
	// is a startup or append error, never a silent split.
	journalMaxFrameBytes = 96 << 20

	journalLogName       = "state.log"
	journalCurrentName   = "CURRENT"
	journalLockName      = "state.lock"
	journalCheckpointDir = "checkpoints"
	journalCurrentMax    = 4 << 10
)

// journalCheckpointLogBytes is the log-size trigger: Store.commitLocked
// compacts as soon as the active log passes it, independently of the 30 s
// timer, so a burst of large frames cannot leave a very long recovery tail. It
// is a variable only so the internal test can lower it instead of writing 64
// MiB; production never changes it.
var journalCheckpointLogBytes int64 = 64 << 20

// ErrStateLocked reports that another process, or another Journal in this
// process, holds the state-directory lock. Startup must fail closed instead of
// waiting, stealing the lock or deleting a stale file.
var ErrStateLocked = errors.New("state directory is already locked")

var journalCRCTable = crc32.MakeTable(crc32.Castagnoli)

// Journal is the append-only commit log of one state directory. It owns the
// directory lock until Close, so no second daemon can open the same state.
//
// OpenJournal recovers and validates the metadata only; Recover applies the
// newest snapshot to a Store and compacts the log. Append and Checkpoint are
// the live-write helpers for AJS-433: the caller must hold the Store write
// lock so a mutation, its snapshot and its frame are ordered, and must not
// call the public Snapshot method while holding it.
type Journal struct {
	mu   sync.Mutex
	dir  string
	lock *journalLock

	// generation is the 128-bit identity of this log lineage. It changes only
	// when a directory is imported for the first time.
	generation [16]byte

	// seq is the last committed sequence: the checkpoint seq at open plus
	// every complete frame replayed from the active log.
	seq uint64
	// checkpointSeq is the seq CURRENT names. It stays below seq while a log
	// suffix is waiting to be checkpointed.
	checkpointSeq uint64

	// snapshot is the newest validated snapshot (the checkpoint document or
	// the last frame body). Recover consumes it and clears it, so a running
	// daemon does not keep a second copy of the whole state.
	snapshot []byte

	// fresh is true when the directory had no CURRENT at open. The next
	// Recover writes generation seq 0 from the Store it is given, which is the
	// one-shot legacy import (or a new install).
	fresh bool

	// log is the open append handle, nil until Recover creates a log for this
	// generation. logExists tracks the active log path independently: during
	// recovery it is scanned read-only before the checkpoint archives it.
	log       *os.File
	logExists bool
	logStart  uint64
	// logBytes is the active log's size, header included. It drives the
	// size-triggered checkpoint so a burst of large frames cannot leave a very
	// long recovery tail between timer ticks.
	logBytes int64

	// frames counts the complete frames replayed by OpenJournal.
	frames int

	// poisoned records the first append failure. A failed write may have left
	// a partial frame; a later append would turn it into interior garbage, so
	// the journal refuses every further write until a restart.
	poisoned error
	closed   bool
}

// JournalRecovery reports what Recover applied. It carries counts and the
// generation only, never snapshot contents.
type JournalRecovery struct {
	Fresh         bool
	Generation    string
	CheckpointSeq uint64
	LastSeq       uint64
	Frames        int
}

// journalCurrent is the parsed CURRENT pointer. The checkpoint filename is
// derived from generation and seq; the file never supplies a path.
type journalCurrent struct {
	generation string
	gen        [16]byte
	seq        uint64
	sum        [32]byte
}

// journalCurrentFile is the on-disk shape of CURRENT. The field order is the
// canonical order written by json.Marshal.
type journalCurrentFile struct {
	Version    int    `json:"version"`
	Generation string `json:"generation"`
	Seq        uint64 `json:"seq"`
	SHA256     string `json:"sha256"`
}

// journalFrame is the decoded body of one state.log frame.
type journalFrame struct {
	Format     int             `json:"format"`
	Generation string          `json:"generation"`
	Seq        uint64          `json:"seq"`
	Snapshot   json.RawMessage `json:"snapshot"`
}

// OpenJournal acquires the state-directory lock and recovers the journal's
// metadata: CURRENT, its checksummed checkpoint and every complete frame of
// the active log. It does not touch a Store; Recover applies the newest
// snapshot. An incomplete final physical frame is discarded; any other
// corruption fails closed. The lock is held until Close.
func OpenJournal(dir string) (*Journal, error) {
	canonical, err := prepareJournalDir(dir)
	if err != nil {
		return nil, err
	}
	lock, err := acquireJournalLock(filepath.Join(canonical, journalLockName))
	if err != nil {
		return nil, err
	}
	j := &Journal{dir: canonical, lock: lock}
	if err := j.load(); err != nil {
		_ = lock.release()
		return nil, err
	}
	return j, nil
}

// prepareJournalDir makes the state directory absolute and ensures it exists.
// The final path component must not be a symlink: the journal and the
// snapshot must share one canonical directory.
func prepareJournalDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("journal directory is empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("journal directory %s: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("journal directory %s: %w", abs, err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("journal directory %s: %w", abs, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("journal directory %s is a symlink", abs)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("journal path %s is not a directory", abs)
	}
	return abs, nil
}

// load recovers CURRENT, the checkpoint it names and the active log. It never
// mutates the journal files: recovery is a read-only pass until Recover
// compacts.
func (j *Journal) load() error {
	currentPath := filepath.Join(j.dir, journalCurrentName)
	data, err := readLimitedFile(currentPath, journalCurrentMax)
	if errors.Is(err, os.ErrNotExist) {
		// A missing pointer is a first run only for a directory with no
		// journal artifacts at all. A surviving checkpoint, archive or active
		// log means the pointer may have been deleted: starting a new
		// generation would silently roll committed state back to state.json.
		artifacts, artifactErr := journalArtifacts(j.dir)
		if artifactErr != nil {
			return artifactErr
		}
		if len(artifacts) > 0 {
			return fmt.Errorf("%s is missing but committed journal files remain (%s); refusing a new generation",
				currentPath, strings.Join(artifacts, ", "))
		}
		j.fresh = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", currentPath, err)
	}
	current, err := parseJournalCurrent(data)
	if err != nil {
		return fmt.Errorf("%s: %w", currentPath, err)
	}
	j.generation = current.gen
	j.seq = current.seq
	j.checkpointSeq = current.seq

	checkpointPath := filepath.Join(j.dir, journalCheckpointDir, checkpointFileName(current.generation, current.seq))
	checkpoint, err := readLimitedFile(checkpointPath, journalMaxFrameBytes)
	if err != nil {
		return fmt.Errorf("checkpoint %s: %w", checkpointPath, err)
	}
	if sum := sha256.Sum256(checkpoint); sum != current.sum {
		return fmt.Errorf("checkpoint %s does not match the sha256 in %s", checkpointPath, currentPath)
	}
	if err := validateSnapshotDocument(checkpoint); err != nil {
		return fmt.Errorf("checkpoint %s: %w", checkpointPath, err)
	}
	j.snapshot = checkpoint

	if err := j.scanLog(); err != nil {
		return err
	}
	return nil
}

// journalArtifacts lists committed journal files other than CURRENT: the
// active log, archived logs and checkpoint documents. Interrupted temporary
// writes (.journal-*.tmp, .state-log-*.tmp) are not committed state and are
// ignored, so a crash during an atomic write does not brick the directory.
func journalArtifacts(dir string) ([]string, error) {
	var found []string
	logs, err := filepath.Glob(filepath.Join(dir, journalLogName+"*"))
	if err != nil {
		return nil, fmt.Errorf("list journal logs in %s: %w", dir, err)
	}
	for _, path := range logs {
		found = append(found, filepath.Base(path))
	}
	checkpointDir := filepath.Join(dir, journalCheckpointDir)
	entries, err := os.ReadDir(checkpointDir)
	if errors.Is(err, os.ErrNotExist) {
		return found, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list checkpoints in %s: %w", checkpointDir, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".journal-") {
			continue
		}
		found = append(found, filepath.Join(journalCheckpointDir, entry.Name()))
	}
	return found, nil
}

// parseJournalCurrent decodes and validates the pointer. Unknown fields,
// uppercase hex, an unsupported version or a malformed digest fail closed.
func parseJournalCurrent(data []byte) (journalCurrent, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return journalCurrent{}, errors.New("pointer is empty")
	}
	var file journalCurrentFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return journalCurrent{}, fmt.Errorf("decode pointer: %w", err)
	}
	if err := requireJSONEOF(dec); err != nil {
		return journalCurrent{}, fmt.Errorf("decode pointer: %w", err)
	}
	if file.Version != journalFormatVersion {
		return journalCurrent{}, fmt.Errorf("unsupported pointer version %d (want %d)", file.Version, journalFormatVersion)
	}
	gen, err := decodeLowerHex(file.Generation, 16)
	if err != nil {
		return journalCurrent{}, fmt.Errorf("pointer generation: %w", err)
	}
	sum, err := decodeLowerHex(file.SHA256, sha256.Size)
	if err != nil {
		return journalCurrent{}, fmt.Errorf("pointer sha256: %w", err)
	}
	var current journalCurrent
	current.generation = file.Generation
	copy(current.gen[:], gen)
	copy(current.sum[:], sum)
	current.seq = file.Seq
	return current, nil
}

// decodeLowerHex accepts exactly n bytes of lowercase hexadecimal. Uppercase
// would make two spellings of one generation or digest possible.
func decodeLowerHex(value string, n int) ([]byte, error) {
	if len(value) != n*2 {
		return nil, fmt.Errorf("%q is not %d lowercase hex characters", value, n*2)
	}
	if strings.ToLower(value) != value {
		return nil, fmt.Errorf("%q is not lowercase hexadecimal", value)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("%q is not hexadecimal", value)
	}
	return decoded, nil
}

// scanLog validates the active log against CURRENT. Frames must be contiguous
// from the header startSeq, belong to the CURRENT generation, carry the
// expected format and decode to a v2 snapshot. A prefix at or below
// CURRENT.seq is allowed (a crash after the checkpoint pointer and before the
// archive); the frames must then reach CURRENT.seq exactly. Only an incomplete
// final physical frame is discarded.
func (j *Journal) scanLog() error {
	path := filepath.Join(j.dir, journalLogName)
	if err := refuseSymlink(path); err != nil {
		return err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		// Crash after archiving the old log and before creating its successor:
		// the checkpoint is complete and there is no suffix to replay.
		return nil
	}
	if err != nil {
		return fmt.Errorf("open journal %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat journal %s: %w", path, err)
	}
	size := info.Size()
	if size < journalHeaderBytes {
		return fmt.Errorf("journal %s has no complete header", path)
	}
	header := make([]byte, journalHeaderBytes)
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("read journal header %s: %w", path, err)
	}
	if got := string(header[:len(journalMagic)]); got != journalMagic {
		return fmt.Errorf("journal %s has magic %q (want %q)", path, got, journalMagic)
	}
	if got := header[len(journalMagic)]; got != journalFormatVersion {
		return fmt.Errorf("journal %s has format %d (want %d)", path, got, journalFormatVersion)
	}
	genOffset := len(journalMagic) + 1
	var gen [16]byte
	copy(gen[:], header[genOffset:genOffset+16])
	if gen != j.generation {
		return fmt.Errorf("journal %s belongs to generation %s, CURRENT names %s",
			path, hex.EncodeToString(gen[:]), j.generationHex())
	}
	startSeq := binary.BigEndian.Uint64(header[genOffset+16:])
	if startSeq == 0 {
		return fmt.Errorf("journal %s has start seq 0", path)
	}
	if startSeq > j.seq+1 {
		return fmt.Errorf("journal %s starts at seq %d, past CURRENT seq %d", path, startSeq, j.seq)
	}
	j.logExists = true
	j.logStart = startSeq

	expected := startSeq
	frames := 0
	offset := int64(journalHeaderBytes)
	for {
		var frameHeader [8]byte
		if _, err := io.ReadFull(file, frameHeader[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				// Fewer than eight bytes remain: the final frame never got a
				// complete header, so it was never acknowledged.
				break
			}
			return fmt.Errorf("read journal %s at offset %d: %w", path, offset, err)
		}
		bodyLen := int64(binary.BigEndian.Uint32(frameHeader[:4]))
		wantCRC := binary.BigEndian.Uint32(frameHeader[4:])
		if bodyLen == 0 || bodyLen > journalMaxFrameBytes {
			return fmt.Errorf("journal %s frame at offset %d has invalid body length %d", path, offset, bodyLen)
		}
		if bodyLen > size-offset-8 {
			// The declared body is short: an incomplete final frame, discarded.
			break
		}
		body := make([]byte, bodyLen)
		if _, err := io.ReadFull(file, body); err != nil {
			return fmt.Errorf("read journal %s frame at offset %d: %w", path, offset, err)
		}
		if got := crc32.Checksum(body, journalCRCTable); got != wantCRC {
			return fmt.Errorf("journal %s frame at offset %d fails CRC32C", path, offset)
		}
		frame, err := decodeJournalFrame(body)
		if err != nil {
			return fmt.Errorf("journal %s frame at offset %d: %w", path, offset, err)
		}
		if frame.Generation != j.generationHex() {
			return fmt.Errorf("journal %s frame at offset %d belongs to generation %q, want %q",
				path, offset, frame.Generation, j.generationHex())
		}
		if frame.Seq != expected {
			return fmt.Errorf("journal %s frame at offset %d has seq %d (want %d)", path, offset, frame.Seq, expected)
		}
		if err := validateSnapshotDocument(frame.Snapshot); err != nil {
			return fmt.Errorf("journal %s frame at offset %d: %w", path, offset, err)
		}
		j.snapshot = append([]byte(nil), frame.Snapshot...)
		expected++
		frames++
		offset += 8 + bodyLen
	}
	last := expected - 1
	if last < j.seq {
		return fmt.Errorf("journal %s ends at seq %d, before CURRENT seq %d", path, last, j.seq)
	}
	if last > j.seq {
		j.seq = last
	}
	j.frames = frames
	return nil
}

// decodeJournalFrame decodes one frame body strictly. The body carries the
// format, generation and sequence again so a bit flip in the CRC-covered bytes
// cannot be mistaken for another record.
func decodeJournalFrame(body []byte) (journalFrame, error) {
	var frame journalFrame
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&frame); err != nil {
		return journalFrame{}, fmt.Errorf("decode body: %w", err)
	}
	if err := requireJSONEOF(dec); err != nil {
		return journalFrame{}, fmt.Errorf("decode body: %w", err)
	}
	if frame.Format != journalFormatVersion {
		return journalFrame{}, fmt.Errorf("unsupported frame format %d (want %d)", frame.Format, journalFormatVersion)
	}
	if len(frame.Snapshot) == 0 {
		return journalFrame{}, errors.New("frame has no snapshot")
	}
	return frame, nil
}

// requireJSONEOF rejects trailing JSON after the first value, so two records
// cannot hide in one frame.
func requireJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing JSON")
		}
		return err
	}
	return nil
}

// validateSnapshotDocument is the cheap structural check shared by frames,
// checkpoints and appends: the document must be JSON with the current version.
// Restore performs the full semantic validation on replay.
func validateSnapshotDocument(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("snapshot is empty")
	}
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("decode snapshot: %w", err)
	}
	if head.Version != SnapshotVersion {
		return fmt.Errorf("snapshot version %d (want %d)", head.Version, SnapshotVersion)
	}
	return nil
}

// Initialized reports whether the directory already holds a committed
// checkpoint. A false result means Recover creates the first generation from
// the Store it is given (legacy state.json import or a new install).
func (j *Journal) Initialized() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return !j.fresh
}

// Generation returns the 128-bit generation as lowercase hex.
func (j *Journal) Generation() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.generationHex()
}

// Seq returns the last committed sequence, including replayed frames.
func (j *Journal) Seq() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.seq
}

func (j *Journal) generationHex() string {
	return hex.EncodeToString(j.generation[:])
}

// Recover applies the newest snapshot to st and compacts the journal: it
// writes an immutable checkpoint at the last sequence, publishes CURRENT,
// archives the replayed log and opens a fresh log at seq+1. On a first run it
// creates generation seq 0 from st instead, which is the one-shot legacy
// import.
//
// The snapshot is validated by a fresh, hook-free Store before st is touched,
// and st must not have any hook installed yet: replay runs no business method
// and no external call. A store with a hook installed is refused rather than
// risking an event from replayed state.
//
// Recover must finish before the Store is attached with AttachJournal: it holds
// the journal lock while it takes the store lock, whereas a live commit takes
// them in the opposite order.
func (j *Journal) Recover(st *Store) (JournalRecovery, error) {
	if st == nil {
		return JournalRecovery{}, errors.New("journal recovery needs a store")
	}
	if st.hooksInstalled() {
		return JournalRecovery{}, errors.New("journal recovery needs a store without hooks installed")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.usableLocked(); err != nil {
		return JournalRecovery{}, err
	}
	if j.fresh {
		data, err := st.Snapshot()
		if err != nil {
			return JournalRecovery{}, err
		}
		if err := j.initLocked(data); err != nil {
			return JournalRecovery{}, err
		}
		report := JournalRecovery{Fresh: true, Generation: j.generationHex()}
		j.snapshot = nil
		return report, nil
	}
	if j.snapshot == nil {
		return JournalRecovery{}, errors.New("journal has no snapshot to recover")
	}
	if err := st.RestoreForReplay(j.snapshot); err != nil {
		return JournalRecovery{}, fmt.Errorf("journal replay at seq %d: %w", j.seq, err)
	}
	if err := j.checkpointLocked(j.snapshot); err != nil {
		return JournalRecovery{}, err
	}
	report := JournalRecovery{
		Generation:    j.generationHex(),
		CheckpointSeq: j.checkpointSeq,
		LastSeq:       j.seq,
		Frames:        j.frames,
	}
	j.snapshot = nil
	return report, nil
}

// Append commits one whole post-mutation snapshot as the next frame. It
// writes the complete frame in one call, checks the short write, fsyncs and
// only then advances the published sequence. The caller must hold the Store
// write lock so the mutation, its snapshot and its frame stay ordered.
func (j *Journal) Append(snapshot []byte) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.usableLocked(); err != nil {
		return 0, err
	}
	if j.fresh || j.log == nil {
		return 0, errors.New("journal is not open for append (Recover has not run)")
	}
	if err := validateSnapshotDocument(snapshot); err != nil {
		return 0, err
	}
	seq := j.seq + 1
	body, err := encodeJournalFrame(j.generationHex(), seq, snapshot)
	if err != nil {
		return 0, err
	}
	if len(body) > journalMaxFrameBytes {
		return 0, fmt.Errorf("journal frame at seq %d is %d bytes, over the %d byte limit", seq, len(body), journalMaxFrameBytes)
	}
	frame := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(body)))
	binary.BigEndian.PutUint32(frame[4:8], crc32.Checksum(body, journalCRCTable))
	copy(frame[8:], body)
	n, err := j.log.Write(frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = j.log.Sync()
	}
	if err != nil {
		// The tail may hold a partial frame and a later append would turn it
		// into interior garbage. Refuse every further write until restart.
		j.poisoned = fmt.Errorf("append journal seq %d: %w", seq, err)
		return 0, j.poisoned
	}
	j.seq = seq
	j.logBytes += int64(len(frame))
	return seq, nil
}

// LogBytes reports the active log's size, header included. The caller uses it
// to compact before the log grows past its bound; it takes the journal lock, so
// it must not be called from inside a journal write.
func (j *Journal) LogBytes() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.logBytes
}

// Checkpoint writes the current post-state at the last committed sequence as
// an immutable checkpoint, publishes CURRENT, archives the active log and
// opens a fresh one at seq+1. The caller must hold the Store write lock.
func (j *Journal) Checkpoint(snapshot []byte) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.usableLocked(); err != nil {
		return 0, err
	}
	if j.fresh || j.log == nil {
		return 0, errors.New("journal is not open for checkpoint (Recover has not run)")
	}
	if err := j.checkpointLocked(snapshot); err != nil {
		return 0, err
	}
	return j.seq, nil
}

// Close releases the active log and the directory lock. It is idempotent.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	var first error
	if j.log != nil {
		if err := j.log.Close(); err != nil {
			first = err
		}
		j.log = nil
	}
	if j.lock != nil {
		if err := j.lock.release(); err != nil && first == nil {
			first = err
		}
		j.lock = nil
	}
	return first
}

// initLocked creates the first generation at seq 0 from data.
func (j *Journal) initLocked(data []byte) error {
	if err := validateSnapshotDocument(data); err != nil {
		return err
	}
	if _, err := rand.Read(j.generation[:]); err != nil {
		return fmt.Errorf("journal generation: %w", err)
	}
	j.seq = 0
	j.checkpointSeq = 0
	if err := j.writeCheckpointFileLocked(data); err != nil {
		return err
	}
	if err := j.writeCurrentLocked(data); err != nil {
		return err
	}
	if err := j.rotateLogLocked(1); err != nil {
		return err
	}
	j.fresh = false
	return nil
}

// checkpointLocked is the compaction sequence: immutable checkpoint, then
// CURRENT, then archive the old log and start a new one. CURRENT is durable
// before the only old log is renamed away.
func (j *Journal) checkpointLocked(data []byte) error {
	if err := validateSnapshotDocument(data); err != nil {
		return err
	}
	if err := j.writeCheckpointFileLocked(data); err != nil {
		return err
	}
	if err := j.writeCurrentLocked(data); err != nil {
		return err
	}
	if err := j.rotateLogLocked(j.seq + 1); err != nil {
		return err
	}
	j.checkpointSeq = j.seq
	return nil
}

// writeCheckpointFileLocked writes checkpoints/<generation>-<seq>.json. The
// name is derived here, never read from a file. An existing checkpoint is
// immutable: a retry may reuse it only when the bytes hash the same.
func (j *Journal) writeCheckpointFileLocked(data []byte) error {
	dir := filepath.Join(j.dir, journalCheckpointDir)
	if err := refuseSymlink(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("checkpoint directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, checkpointFileName(j.generationHex(), j.seq))
	sum := sha256.Sum256(data)
	existing, err := readLimitedFile(path, journalMaxFrameBytes)
	switch {
	case err == nil:
		if sha256.Sum256(existing) != sum {
			return fmt.Errorf("checkpoint %s already exists with different contents", path)
		}
		return nil
	case errors.Is(err, os.ErrNotExist):
	default:
		return fmt.Errorf("checkpoint %s: %w", path, err)
	}
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("checkpoint %s: %w", path, err)
	}
	return nil
}

// writeCurrentLocked publishes the checkpoint pointer atomically. It is the
// commit point of a checkpoint.
func (j *Journal) writeCurrentLocked(data []byte) error {
	path := filepath.Join(j.dir, journalCurrentName)
	if err := refuseSymlink(path); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	payload, err := json.Marshal(journalCurrentFile{
		Version:    journalFormatVersion,
		Generation: j.generationHex(),
		Seq:        j.seq,
		SHA256:     hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	payload = append(payload, '\n')
	if err := writeFileAtomic(path, payload, 0o600); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// rotateLogLocked archives the active log as
// state.log.<generation>.<start>-<seq> and creates the next one. The archive
// name is redundant with the checkpoint at seq, so an existing file with the
// same name (a repeated recovery at one sequence) is replaced; it is never the
// only copy of a committed state.
func (j *Journal) rotateLogLocked(nextStart uint64) error {
	logPath := filepath.Join(j.dir, journalLogName)
	if j.log != nil {
		if err := j.log.Close(); err != nil {
			j.log = nil
			return fmt.Errorf("close journal %s: %w", logPath, err)
		}
		j.log = nil
	}
	if j.logExists {
		archive := filepath.Join(j.dir, fmt.Sprintf("%s.%s.%d-%d",
			journalLogName, j.generationHex(), j.logStart, j.seq))
		if err := refuseSymlink(logPath); err != nil {
			return err
		}
		if err := os.Remove(archive); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("replace archived journal %s: %w", archive, err)
		}
		if err := os.Rename(logPath, archive); err != nil {
			return fmt.Errorf("archive journal %s: %w", logPath, err)
		}
		syncDir(j.dir)
		j.logExists = false
	}
	return j.createLogLocked(nextStart)
}

// createLogLocked writes the generation header of a new active log. The
// header is written to a temporary file and renamed into place, so a crash
// during creation leaves either no state.log (a checkpoint-only recovery) or a
// complete header, never a truncated one that would fail startup forever.
func (j *Journal) createLogLocked(startSeq uint64) error {
	path := filepath.Join(j.dir, journalLogName)
	if err := refuseSymlink(path); err != nil {
		return err
	}
	header := make([]byte, journalHeaderBytes)
	copy(header, journalMagic)
	header[len(journalMagic)] = journalFormatVersion
	copy(header[len(journalMagic)+1:], j.generation[:])
	binary.BigEndian.PutUint64(header[len(journalMagic)+1+16:], startSeq)

	tmp, err := os.CreateTemp(j.dir, ".state-log-*.tmp")
	if err != nil {
		return fmt.Errorf("create journal %s: %w", path, err)
	}
	tmpName := tmp.Name()
	discard := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return discard(err)
	}
	if _, err := tmp.Write(header); err != nil {
		return discard(fmt.Errorf("write journal header %s: %w", path, err))
	}
	if err := tmp.Sync(); err != nil {
		return discard(fmt.Errorf("sync journal header %s: %w", path, err))
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close journal header %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("install journal %s: %w", path, err)
	}
	syncDir(j.dir)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open journal %s: %w", path, err)
	}
	j.log = file
	j.logExists = true
	j.logStart = startSeq
	j.logBytes = journalHeaderBytes
	return nil
}

// encodeJournalFrame renders one canonical body with the raw snapshot
// embedded. json.Marshal validates and compacts the raw document.
func encodeJournalFrame(generation string, seq uint64, snapshot []byte) ([]byte, error) {
	body, err := json.Marshal(journalFrame{
		Format:     journalFormatVersion,
		Generation: generation,
		Seq:        seq,
		Snapshot:   json.RawMessage(snapshot),
	})
	if err != nil {
		return nil, fmt.Errorf("encode journal frame: %w", err)
	}
	return body, nil
}

func (j *Journal) usableLocked() error {
	if j.closed {
		return errors.New("journal is closed")
	}
	if j.poisoned != nil {
		return j.poisoned
	}
	return nil
}

func checkpointFileName(generation string, seq uint64) string {
	return fmt.Sprintf("%s-%d.json", generation, seq)
}

// refuseSymlink rejects a managed path that is a symlink. The journal never
// follows one, even when the target would be inside the same directory.
func refuseSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", path)
	}
	return nil
}

// readLimitedFile reads a regular file, refusing a symlink or an oversized
// document before allocating it.
func readLimitedFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s is %d bytes, over the %d byte limit", path, info.Size(), limit)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is %d bytes, over the %d byte limit", path, len(data), limit)
	}
	return data, nil
}

// writeFileAtomic writes data through a 0600 temporary file in the target
// directory, syncs it, closes it and renames it over path. The parent
// directory is synced best effort (Windows rejects opening a directory).
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".journal-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	discard := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		return discard(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return discard(err)
	}
	if err := tmp.Sync(); err != nil {
		return discard(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir makes a rename durable where the platform supports it. Windows
// rejects opening a directory, so the error is intentionally ignored.
func syncDir(dir string) {
	if handle, err := os.Open(dir); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
}
