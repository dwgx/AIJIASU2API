package schedule

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ajsaccountd/internal/model"
)

// FireRetention bounds the in-memory and on-disk fire journal.
const FireRetention = 1000

// fireLine is one JSONL journal record.
type fireLine struct {
	Op            string `json:"op"` // reserve|finish
	Key           string `json:"key"`
	ScheduleID    string `json:"scheduleId,omitempty"`
	ScheduledUnix int64  `json:"scheduledUnix,omitempty"`
	RunID         string `json:"runId,omitempty"`
	Owner         string `json:"owner,omitempty"`
	State         string `json:"state,omitempty"`
	At            string `json:"at,omitempty"`
}

// FireLog is the append-only, fsynced fire journal. Reserve is the durable
// at-most-once gate: it returns false for a key that was already reserved, and
// a reservation without a terminal finish becomes unknown on the next open
// instead of being retried.
type FireLog struct {
	mu      sync.Mutex
	path    string
	file    *os.File
	records map[string]model.ScheduleFire
	order   []string
	unknown []model.ScheduleFire
}

// OpenFireLog opens or creates the journal. An empty path keeps the log in
// memory (tests only); the daemon always passes <state dir>/schedule-fire.log.
func OpenFireLog(path string, now func() time.Time) (*FireLog, error) {
	if now == nil {
		now = time.Now
	}
	log := &FireLog{path: path, records: make(map[string]model.ScheduleFire)}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("schedule fire log directory: %w", err)
		}
		if err := log.replay(); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, fmt.Errorf("schedule fire log: %w", err)
		}
		log.file = file
		// A reservation left by a dead process is unknown, never retried.
		for _, key := range append([]string(nil), log.order...) {
			record := log.records[key]
			if record.State != "reserved" {
				continue
			}
			record.State = "unknown"
			record.FinishedAt = now().UTC().Format(time.RFC3339Nano)
			log.records[key] = record
			log.unknown = append(log.unknown, record)
			if err := log.appendLocked(fireLine{Op: "finish", Key: key, State: "unknown", At: record.FinishedAt}); err != nil {
				_ = log.file.Close()
				return nil, err
			}
		}
	}
	log.pruneLocked(now())
	return log, nil
}

// Reserve durably claims key. It returns false when the key was already
// reserved (a replay), and the error when the write or fsync failed; in both
// cases the caller must not execute the action.
func (l *FireLog) Reserve(key, scheduleID string, scheduledUnix int64, runID, owner string, at time.Time) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.records[key]; exists {
		return false, nil
	}
	record := model.ScheduleFire{
		Key:           key,
		ScheduleID:    scheduleID,
		ScheduledUnix: scheduledUnix,
		State:         "reserved",
		RunID:         runID,
		Owner:         owner,
		ReservedAt:    at.UTC().Format(time.RFC3339Nano),
	}
	if err := l.appendLocked(fireLine{
		Op:            "reserve",
		Key:           key,
		ScheduleID:    scheduleID,
		ScheduledUnix: scheduledUnix,
		RunID:         runID,
		Owner:         owner,
		At:            record.ReservedAt,
	}); err != nil {
		return false, err
	}
	l.records[key] = record
	l.order = append(l.order, key)
	l.pruneLocked(at)
	return true, nil
}

// Finish records the terminal state. A failed write leaves the reservation in
// place; the next open turns it into unknown, which is the conservative result.
func (l *FireLog) Finish(key, state string, at time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	record, ok := l.records[key]
	if !ok {
		return fmt.Errorf("fire key %q was not reserved", key)
	}
	record.State = state
	record.FinishedAt = at.UTC().Format(time.RFC3339Nano)
	l.records[key] = record
	return l.appendLocked(fireLine{Op: "finish", Key: key, State: state, At: record.FinishedAt})
}

// Records returns a copy of the retained fire records.
func (l *FireLog) Records() []model.ScheduleFire {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]model.ScheduleFire, 0, len(l.records))
	for _, key := range l.order {
		out = append(out, l.records[key])
	}
	return out
}

// TakeUnknown returns the reservations that were converted to unknown by the
// most recent Open, and clears the list. The engine audits each one once.
func (l *FireLog) TakeUnknown() []model.ScheduleFire {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.unknown
	l.unknown = nil
	return out
}

// Compact rewrites the journal from keep and truncates the old records. The
// caller runs it after a successful snapshot, so the snapshot is the
// checkpoint and the journal only covers the interval after it.
func (l *FireLog) Compact(keep []model.ScheduleFire) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = make(map[string]model.ScheduleFire, len(keep))
	l.order = l.order[:0]
	for _, record := range keep {
		l.records[record.Key] = record
		l.order = append(l.order, record.Key)
	}
	if l.path == "" {
		return nil
	}
	tmpPath := l.path + ".tmp"
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("schedule fire log compact: %w", err)
	}
	writer := bufio.NewWriter(tmp)
	encode := func(line fireLine) error {
		data, err := json.Marshal(line)
		if err != nil {
			return err
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return err
		}
		return nil
	}
	for _, key := range l.order {
		record := l.records[key]
		if err := encode(fireLine{Op: "reserve", Key: key, ScheduleID: record.ScheduleID,
			ScheduledUnix: record.ScheduledUnix, RunID: record.RunID, Owner: record.Owner,
			At: record.ReservedAt}); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("schedule fire log compact: %w", err)
		}
		if record.State != "reserved" {
			if err := encode(fireLine{Op: "finish", Key: key, State: record.State, At: record.FinishedAt}); err != nil {
				_ = tmp.Close()
				_ = os.Remove(tmpPath)
				return fmt.Errorf("schedule fire log compact: %w", err)
			}
		}
	}
	if err := writer.Flush(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("schedule fire log compact: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("schedule fire log compact: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("schedule fire log compact: %w", err)
	}
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	// The old file was closed above; os.Rename replaces it. If the rename
	// fails the old journal is still intact, so no reservation is lost.
	if err := os.Rename(tmpPath, l.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("schedule fire log compact: %w", err)
	}
	// Best effort: make the rename durable where the OS supports a directory
	// sync. Windows rejects opening a directory, so the error is ignored.
	if dir, err := os.Open(filepath.Dir(l.path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("schedule fire log: %w", err)
	}
	l.file = file
	return nil
}

// Close flushes and closes the journal file.
func (l *FireLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func (l *FireLog) replay() error {
	file, err := os.Open(l.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("schedule fire log: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		raw, readErr := reader.ReadBytes('\n')
		line := strings.TrimSpace(string(raw))
		if len(line) > 0 {
			var entry fireLine
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				if readErr == io.EOF {
					// A torn final line is a crash mid-append; ignore it.
					break
				}
				return fmt.Errorf("schedule fire log: %w", err)
			}
			l.applyLocked(entry)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return fmt.Errorf("schedule fire log: %w", readErr)
		}
	}
	return nil
}

func (l *FireLog) applyLocked(entry fireLine) {
	if entry.Key == "" {
		return
	}
	record, exists := l.records[entry.Key]
	if !exists {
		record = model.ScheduleFire{
			Key:           entry.Key,
			ScheduleID:    entry.ScheduleID,
			ScheduledUnix: entry.ScheduledUnix,
			State:         "reserved",
			RunID:         entry.RunID,
			Owner:         entry.Owner,
			ReservedAt:    entry.At,
		}
		l.records[entry.Key] = record
		l.order = append(l.order, entry.Key)
	}
	if entry.Op == "finish" {
		record.State = entry.State
		record.FinishedAt = entry.At
		l.records[entry.Key] = record
	}
}

func (l *FireLog) appendLocked(entry fireLine) error {
	if l.file == nil {
		return nil
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := l.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("schedule fire log write: %w", err)
	}
	if entry.Op == "reserve" {
		// The reservation is the at-most-once boundary: it must survive a
		// crash before the action starts. A finish is best effort.
		if err := l.file.Sync(); err != nil {
			return fmt.Errorf("schedule fire log sync: %w", err)
		}
	}
	return nil
}

func (l *FireLog) pruneLocked(now time.Time) {
	cutoff := now.Add(-24 * time.Hour)
	for len(l.order) > FireRetention {
		removed := false
		for i, key := range l.order {
			if l.records[key].State == "reserved" {
				continue
			}
			delete(l.records, key)
			l.order = append(l.order[:i], l.order[i+1:]...)
			removed = true
			break
		}
		if !removed {
			break
		}
	}
	kept := l.order[:0]
	for _, key := range l.order {
		record := l.records[key]
		if record.State != "reserved" {
			if finished, err := time.Parse(time.RFC3339Nano, record.FinishedAt); err == nil && finished.Before(cutoff) {
				delete(l.records, key)
				continue
			}
		}
		kept = append(kept, key)
	}
	l.order = kept
}
