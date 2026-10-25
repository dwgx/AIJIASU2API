// Panel backup HTTP handlers.
//
// The envelope format, the KDF and the diff live in internal/backup; these
// handlers own only the HTTP contract: the busy lock, the size caps, the LIVE
// node refusal and the atomic writes into the daemon's state directory. A
// backup carries sealed credentials, so no handler here logs a path, a blob or
// a passphrase.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ajsaccountd/internal/backup"
	"ajsaccountd/internal/model"
)

// Backup error codes. They extend the shared contract codes because each one
// asks the operator for a different action.
const (
	// CodeBackupBusy: a backup or restore already holds the panel-file lock.
	CodeBackupBusy = "BACKUP_BUSY"
	// CodeBackupLiveNode: nodes are LIVE, so a restore would replace the state
	// that describes their open gates.
	CodeBackupLiveNode = "BACKUP_LIVE_NODE"
	// CodeBackupDaemonRunning: an apply was requested while the daemon runs and
	// AJS_BACKUP_ALLOW_RESTORE is not set.
	CodeBackupDaemonRunning = "BACKUP_DAEMON_RUNNING"
	// CodeBackupUnavailable: the daemon was started without a state directory,
	// so there is nothing to back up or restore.
	CodeBackupUnavailable = "BACKUP_UNAVAILABLE"
)

// backupConfirm is the exact string an apply must send. A restore replaces
// every panel file, so a boolean would be too easy to send by accident.
const backupConfirm = "RESTORE"

// maxBackupBodyBytes bounds the two routes that carry an envelope. The
// envelope itself is capped at backup.MaxEnvelopeBytes; the extra megabyte
// covers the JSON wrapper (passphrase, dryRun, confirm).
const maxBackupBodyBytes int64 = backup.MaxEnvelopeBytes + (1 << 20)

// RestoreFlag is the single bit of state the HTTP server shares with the
// process that owns state.json. A successful apply marks it; from then on the
// daemon's snapshot writer refuses every save, because the in-memory store
// still holds the panel the restore replaced and writing it would undo the
// restore. The zero value is ready to use.
//
// The flag lives here because only the restore handler sets it. The daemon
// passes one instance to both Config and its snapshot writer, so the server
// and the writer cannot disagree about whether a restore happened.
type RestoreFlag struct {
	restored atomic.Bool
}

// MarkRestored records that a restore has replaced the panel files.
func (f *RestoreFlag) MarkRestored() { f.restored.Store(true) }

// Restored reports whether a restore has replaced the panel files.
func (f *RestoreFlag) Restored() bool { return f.restored.Load() }

type backupCreateRequest struct {
	Passphrase string `json:"passphrase"`
}

type backupInspectRequest struct {
	// Envelope is the envelope document itself, not a base64 string.
	Envelope   json.RawMessage `json:"envelope"`
	Passphrase string          `json:"passphrase,omitempty"`
}

type backupRestoreRequest struct {
	Envelope   json.RawMessage `json:"envelope"`
	Passphrase string          `json:"passphrase"`
	// DryRun defaults to true: an omitted field must never overwrite a panel.
	DryRun  *bool  `json:"dryRun,omitempty"`
	Confirm string `json:"confirm,omitempty"`
}

// backupDiffView is the JSON projection of backup.DiffReport. The package type
// carries no JSON tags because it is also used in-process, so the wire names
// live here.
type backupDiffView struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Changed int `json:"changed"`
	Same    int `json:"same"`

	AddedBytes   int64 `json:"addedBytes"`
	RemovedBytes int64 `json:"removedBytes"`
	ChangedBytes int64 `json:"changedBytes"`
	SameBytes    int64 `json:"sameBytes"`

	AddedNames   []string `json:"addedNames"`
	RemovedNames []string `json:"removedNames"`
	ChangedNames []string `json:"changedNames"`
	SameNames    []string `json:"sameNames"`
}

// backupInspectView is the unencrypted part of an envelope. Manifest is
// present only when the caller supplied the passphrase: the manifest lives
// inside the encrypted payload, so a header-only inspect cannot report it.
type backupInspectView struct {
	Version       int              `json:"version"`
	Supported     bool             `json:"supported"`
	App           string           `json:"app"`
	CreatedAt     time.Time        `json:"createdAt"`
	EnvelopeBytes int              `json:"envelopeBytes"`
	Manifest      *backup.Manifest `json:"manifest,omitempty"`
}

type backupRestoreView struct {
	DryRun  bool `json:"dryRun"`
	Applied bool `json:"applied"`
	// RestartRequired is true after an apply: the running daemon still holds
	// the replaced panel in memory and must be restarted.
	RestartRequired bool `json:"restartRequired"`
	// ExitRecommended is true after an apply: the restored panel is on disk,
	// this process has stopped writing snapshots and cannot serve the
	// restored state, so the operator should stop it now.
	ExitRecommended bool            `json:"exitRecommended"`
	Manifest        backup.Manifest `json:"manifest"`
	Diff            backupDiffView  `json:"diff"`
	// Files lists the blobs an apply wrote. It is empty for a dry run.
	Files []string `json:"files,omitempty"`
	// Warning tells the operator what the response alone cannot: a dry run
	// changed nothing, and an apply needs a restart.
	Warning string `json:"warning,omitempty"`
}

// handleBackupCreate seals the panel files into an encrypted envelope and
// serves it as a download. The envelope is built completely in memory before
// any header is written, so an encryption failure never leaves a truncated
// backup on the operator's disk.
func (s *Server) handleBackupCreate(w http.ResponseWriter, r *http.Request) {
	var req backupCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateBackupPassphrase(req.Passphrase); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
		return
	}
	if !s.backupAvailable(w) {
		return
	}
	if !s.backupMu.TryLock() {
		writeBackupError(w, http.StatusConflict, CodeBackupBusy,
			"another backup or restore is already running", nil)
		return
	}
	defer s.backupMu.Unlock()

	start := time.Now()
	// A backup must describe the panel as it is now, not as it was up to one
	// snapshot interval ago. Persist is best-effort and nil in a store-only
	// server; either way the files on disk are what gets sealed.
	if s.cfg.Persist != nil {
		s.cfg.Persist()
	}
	blobs, err := s.readPanelBlobs()
	if err != nil {
		s.logger.Error("backup_create_failed", "stage", "read", "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
		return
	}
	defer clearBlobs(blobs)
	if _, ok := blobs[backup.BlobState]; !ok {
		writeError(w, http.StatusConflict, CodeConflict,
			"state.json is missing; the daemon has not written a snapshot yet")
		return
	}

	envelope, manifest, err := backup.Create(blobs, req.Passphrase, time.Now())
	req.Passphrase = ""
	if err != nil {
		if errors.Is(err, backup.ErrTooLarge) {
			writeError(w, http.StatusConflict, CodeConflict, err.Error())
			return
		}
		s.logger.Error("backup_create_failed", "stage", "seal", "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
		return
	}

	s.store.RecordAudit(model.SeverityInfo, "operator", "backup.create", "backup",
		fmt.Sprintf("blobs=%d bytes=%d", len(manifest.Entries), len(envelope)))
	s.logger.Info("backup_created",
		"blobs", len(manifest.Entries),
		"bytes", len(envelope),
		"duration_ms", time.Since(start).Milliseconds(),
	)

	filename := "ajs-backup-" + time.Now().UTC().Format("20060102-150405") + ".ajsb"
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(envelope)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-AJS-Backup-Blobs", strconv.Itoa(len(manifest.Entries)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(envelope)
}

// handleBackupInspect reports the envelope header and, when the passphrase is
// supplied, the manifest. Neither form returns a blob, and neither touches the
// state directory, so inspect works on a daemon that cannot back up.
func (s *Server) handleBackupInspect(w http.ResponseWriter, r *http.Request) {
	var req backupInspectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !checkBackupEnvelope(w, req.Envelope) {
		return
	}
	header, err := backup.Inspect(req.Envelope)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
		return
	}
	// A newer daemon keeps the same app name, so this rejects a foreign JSON
	// document without hiding a backup written by a future version.
	if header.App != backup.AppName {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "envelope is not an apc panel backup")
		return
	}
	view := backupInspectView{
		Version:       header.Version,
		Supported:     header.Version == backup.Version,
		App:           header.App,
		CreatedAt:     header.CreatedAt,
		EnvelopeBytes: len(req.Envelope),
	}
	if req.Passphrase != "" {
		if err := validateBackupPassphrase(req.Passphrase); err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
			return
		}
		blobs, manifest, err := backup.Open(req.Envelope, req.Passphrase)
		req.Passphrase = ""
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
			return
		}
		clearBlobs(blobs)
		view.Manifest = &manifest
	}
	writeJSON(w, http.StatusOK, view)
}

// handleBackupRestore compares an envelope with the panel files and, when the
// caller asks for it explicitly, replaces them. dryRun defaults to true, an
// apply needs confirm:"RESTORE", and an apply is refused while the daemon runs
// unless AJS_BACKUP_ALLOW_RESTORE=1, because the running process cannot serve
// the restored panel. A successful apply marks the shared RestoreFlag, which
// makes the daemon stop writing state.json: the in-memory store still holds
// the panel the restore replaced, so a later snapshot (including the shutdown
// save) would undo the restore. The response says exitRecommended:true.
func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	var req backupRestoreRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// Both a dry run and an apply compare against the live state directory, so
	// a daemon without one cannot serve either.
	if !s.backupAvailable(w) {
		return
	}
	dryRun := true
	if req.DryRun != nil {
		dryRun = *req.DryRun
	}
	if !dryRun {
		if req.Confirm != backupConfirm {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument,
				`confirm must be "`+backupConfirm+`" when dryRun is false`)
			return
		}
		if !s.cfg.AllowBackupRestore {
			writeBackupError(w, http.StatusConflict, CodeBackupDaemonRunning,
				"restore must run while the daemon is stopped: set AJS_BACKUP_ALLOW_RESTORE=1 to write the files while it runs and restart afterwards",
				nil)
			return
		}
	}
	if err := validateBackupPassphrase(req.Passphrase); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
		return
	}
	if !checkBackupEnvelope(w, req.Envelope) {
		return
	}
	if !s.backupMu.TryLock() {
		writeBackupError(w, http.StatusConflict, CodeBackupBusy,
			"another backup or restore is already running", nil)
		return
	}
	defer s.backupMu.Unlock()

	restored, manifest, err := backup.Open(req.Envelope, req.Passphrase)
	req.Passphrase = ""
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
		return
	}
	defer clearBlobs(restored)

	current, err := s.readPanelBlobs()
	if err != nil {
		s.logger.Error("backup_restore_failed", "stage", "read", "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
		return
	}
	defer clearBlobs(current)

	view := backupRestoreView{
		DryRun:   dryRun,
		Manifest: manifest,
		Diff:     toBackupDiffView(backup.Diff(current, restored)),
	}
	if dryRun {
		view.Warning = "dry run only: no file was written; send dryRun:false with confirm:\"" + backupConfirm + "\" to apply"
		writeJSON(w, http.StatusOK, view)
		return
	}
	if _, ok := restored[backup.BlobState]; !ok {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"backup has no state.json; refusing to restore a partial panel")
		return
	}
	// Checked last, immediately before the write: a node that goes LIVE during
	// the KDF must still block the restore.
	if live := s.liveNodeIDs(); len(live) > 0 {
		writeBackupError(w, http.StatusConflict, CodeBackupLiveNode,
			"refusing to restore while proxy nodes are LIVE; drain or stop them first",
			map[string]string{"liveNodes": strconv.Itoa(len(live)), "firstLiveNode": live[0]})
		return
	}

	written, err := s.writePanelBlobs(restored)
	if err != nil {
		s.logger.Error("backup_restore_failed", "stage", "write", "written", len(written), "error", err)
		writeBackupError(w, http.StatusInternalServerError, CodeInternal,
			"restore failed while writing the panel files; the state directory is inconsistent until a restore succeeds or the daemon restarts",
			map[string]string{"written": strings.Join(written, ",")})
		return
	}

	// The files on disk are now the restored panel while the store still holds
	// the replaced one. Marking the shared flag before anything else makes
	// every later snapshot a no-op, so no interval tick, bulk persist or
	// shutdown save can write the stale panel back over the restore.
	s.restore.MarkRestored()

	s.store.RecordAudit(model.SeverityWarn, "operator", "backup.restore", "backup",
		fmt.Sprintf("blobs=%d changed=%d added=%d removed=%d",
			len(written), view.Diff.Changed, view.Diff.Added, view.Diff.Removed))
	s.logger.Warn("backup_restored",
		"blobs", len(written),
		"changed", view.Diff.Changed,
		"added", view.Diff.Added,
		"removed", view.Diff.Removed,
		"restart_required", true,
	)

	view.Applied = true
	view.RestartRequired = true
	view.ExitRecommended = true
	view.Files = written
	view.Warning = "the panel files were replaced and this process has stopped writing snapshots; stop it now and start it again to serve the restored panel"
	writeJSON(w, http.StatusOK, view)
}

// backupAvailable answers 503 when the daemon was started without a state
// directory, where there is nothing to back up. It reports whether the handler
// may continue.
func (s *Server) backupAvailable(w http.ResponseWriter) bool {
	if strings.TrimSpace(s.cfg.StateDir) != "" {
		return true
	}
	writeBackupError(w, http.StatusServiceUnavailable, CodeBackupUnavailable,
		"backup routes are disabled: the daemon has no state directory", nil)
	return false
}

// checkBackupEnvelope rejects an empty or oversized envelope before the
// package parses it. The cap matches the package's own, so a body that fits the
// route limit can still be refused as an impossible backup.
func checkBackupEnvelope(w http.ResponseWriter, envelope json.RawMessage) bool {
	switch {
	case len(envelope) == 0:
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "envelope is required")
		return false
	case len(envelope) > backup.MaxEnvelopeBytes:
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			fmt.Sprintf("envelope exceeds the %d byte cap", backup.MaxEnvelopeBytes))
		return false
	}
	return true
}

// readPanelBlobs loads the panel files that exist. A file the daemon never
// wrote (no master key, no device pool) is simply absent, which the diff
// reports as added or unchanged rather than as an error.
func (s *Server) readPanelBlobs() (map[string][]byte, error) {
	blobs := make(map[string][]byte, 3)
	for _, name := range backupBlobNames {
		path, err := s.backupPath(name)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		blobs[name] = data
	}
	return blobs, nil
}

// writePanelBlobs replaces the panel files an envelope carries. It never
// deletes a file the envelope omits: an older backup without secrets.json must
// not silently drop the live sealed secrets. Files are written in a fixed
// order so a failure reports a stable prefix of the set.
func (s *Server) writePanelBlobs(blobs map[string][]byte) ([]string, error) {
	written := make([]string, 0, len(blobs))
	for _, name := range backupBlobNames {
		data, ok := blobs[name]
		if !ok {
			continue
		}
		path, err := s.backupPath(name)
		if err != nil {
			return written, err
		}
		if err := writeFileAtomic(path, data, 0o600); err != nil {
			return written, err
		}
		written = append(written, name)
	}
	return written, nil
}

// backupBlobNames is the order blobs are read and written in: the panel state
// first, then the two sealed-secret files.
var backupBlobNames = []string{backup.BlobState, backup.BlobSecrets, backup.BlobDeviceSecrets}

// backupPath resolves one blob name to the file the daemon uses. state.json
// follows AJS_STATE_FILE when it is set, so a backup of a relocated state file
// restores to the same place.
func (s *Server) backupPath(name string) (string, error) {
	switch name {
	case backup.BlobState:
		if path := strings.TrimSpace(s.cfg.StatePath); path != "" {
			return path, nil
		}
		return filepath.Join(s.cfg.StateDir, backup.BlobState), nil
	case backup.BlobSecrets, backup.BlobDeviceSecrets:
		return filepath.Join(s.cfg.StateDir, name), nil
	default:
		return "", fmt.Errorf("backup: unknown blob name %q", name)
	}
}

// liveNodeIDs lists the nodes holding an open gate, sorted for a stable
// response. A restore replaces the state file that describes them.
func (s *Server) liveNodeIDs() []string {
	var live []string
	for _, node := range s.store.Nodes() {
		if node.State == model.NodeLive {
			live = append(live, node.ID)
		}
	}
	sort.Strings(live)
	return live
}

// writeFileAtomic replaces path with data through a 0600 temporary file in the
// same directory, so a crash mid-write cannot leave a truncated state file and
// a concurrent reader never observes a partial document.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("backup: state dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("backup: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("backup: chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("backup: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("backup: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("backup: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("backup: rename: %w", err)
	}
	return nil
}

// validateBackupPassphrase applies the package's bounds up front so a weak
// passphrase answers 400 without reading or hashing anything.
func validateBackupPassphrase(passphrase string) error {
	size := len(passphrase)
	if size < backup.MinPassphraseBytes || size > backup.MaxPassphraseBytes {
		return fmt.Errorf("passphrase must be %d to %d bytes", backup.MinPassphraseBytes, backup.MaxPassphraseBytes)
	}
	return nil
}

// clearBlobs zeroes the plaintext blobs once the envelope has been sealed or
// the files have been written, so credentials do not linger in memory.
func clearBlobs(blobs map[string][]byte) {
	for _, data := range blobs {
		clear(data)
	}
}

func toBackupDiffView(report backup.DiffReport) backupDiffView {
	return backupDiffView{
		Added:        report.Added,
		Removed:      report.Removed,
		Changed:      report.Changed,
		Same:         report.Same,
		AddedBytes:   report.AddedBytes,
		RemovedBytes: report.RemovedBytes,
		ChangedBytes: report.ChangedBytes,
		SameBytes:    report.SameBytes,
		AddedNames:   nonNilStrings(report.AddedNames),
		RemovedNames: nonNilStrings(report.RemovedNames),
		ChangedNames: nonNilStrings(report.ChangedNames),
		SameNames:    nonNilStrings(report.SameNames),
	}
}

// nonNilStrings keeps an empty list as [] rather than null in the JSON body.
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// writeBackupError writes the shared error envelope with the optional details
// map the backup codes use to name the blocking node or the files already
// written.
func writeBackupError(w http.ResponseWriter, status int, code, message string, details map[string]string) {
	writeJSON(w, status, errorResponse{Error: errorDetail{
		Code:    code,
		Message: message,
		Details: details,
	}})
}
