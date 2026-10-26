// Account import/export and bulk-delete helpers.
//
// This file owns the pure account-IO domain: the versioned file formats, the
// encrypted envelope, validation, merge planning and the store-side apply.
// The HTTP layer in internal/api/account_io.go only parses the wrapper and
// translates errors; keeping the format logic here lets the security tests
// call it directly.
//
// No function in this file logs. Credentials and passphrases are never part of
// an error, a result row or an audit event.
package store

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ajsaccountd/internal/model"
)

// Account-IO limits. They bound parsing, memory and the sequential delete
// loop. The import body is larger than the ordinary 1 MiB route limit because
// base64 and JSON overhead can inflate a 1 MiB decoded document.
const (
	AccountExportFormat    = "ajs-accounts"
	AccountExportVersion   = 1
	EncryptedExportFormat  = "ajs-accounts-encrypted"
	ImportResultFormat     = "ajs-accounts-import-result"
	BulkDeleteResultFormat = "ajs-accounts-bulk-delete-result"

	MaxImportRows          = 1000
	MaxExportAccounts      = 1000
	MaxDeleteIDs           = 100
	MaxDecodedExportBytes  = 1 << 20
	MaxImportBodyBytes     = 4 << 20
	MaxPasswordBytes       = 512
	MaxLabelBytes          = 80
	MaxUsernameBytes       = 80
	MaxRegionBytes         = 64
	MaxRegionCount         = 64
	MaxPassphraseBytes     = 1024
	MinPassphraseBytes     = 12
	ExportPBKDF2Iterations = 600000
	MinPBKDF2Iterations    = 100000
	MaxPBKDF2Iterations    = 2000000
)

// WarningCode is a structured, non-secret note attached to an export row or
// an import result. The dashboard maps it to an i18n string.
type WarningCode string

const (
	WarningPasswordKept           WarningCode = "password_kept"
	WarningPasswordUnavailable    WarningCode = "password_unavailable"
	WarningDeviceKept             WarningCode = "device_kept"
	WarningDeviceRestored         WarningCode = "device_restored"
	WarningDeviceGeneratedOnLogin WarningCode = "device_generated_on_login"
	WarningDeviceReplaced         WarningCode = "device_replaced"
	WarningIDIgnored              WarningCode = "id_ignored"
	WarningPlatformDeviceMismatch WarningCode = "platform_device_mismatch"
	WarningCountsMismatch         WarningCode = "counts_mismatch"
	WarningUnknownColumn          WarningCode = "unknown_column"
)

// Row issue and result codes. They are stable API vocabulary.
const (
	CodeInvalidArgument  = "INVALID_ARGUMENT"
	CodeConflict         = "CONFLICT"
	CodeDuplicateRow     = "DUPLICATE_ROW"
	CodePasswordRequired = "PASSWORD_REQUIRED"
	CodeSecretStorageOff = "SECRET_STORAGE_DISABLED"
	CodeDeviceInUse      = "DEVICE_IN_USE"
	CodeNotFound         = "NOT_FOUND"
)

// RowIssue is one row-level validation failure. Message never contains a
// password or passphrase.
type RowIssue struct {
	Code    string
	Message string
}

// AccountExportSource carries informational provenance.
type AccountExportSource struct {
	DaemonVersion string `json:"daemonVersion,omitempty"`
	SecretsSealed bool   `json:"secretsSealed"`
}

// AccountExportCounts is informational; import recomputes its own counts.
type AccountExportCounts struct {
	Accounts  int `json:"accounts"`
	Passwords int `json:"passwords"`
	Devices   int `json:"devices"`
}

// AccountExport is the plaintext JSON document. Password is null when the
// source daemon had no sealed secret for the account.
type AccountExport struct {
	Format     string              `json:"format"`
	Version    int                 `json:"version"`
	ExportedAt string              `json:"exportedAt"`
	Source     AccountExportSource `json:"source"`
	Counts     AccountExportCounts `json:"counts"`
	Accounts   []AccountExportRow  `json:"accounts"`
}

// AccountExportRow is one account in a plaintext document. Device is omitted
// when the export did not request device identity.
type AccountExportRow struct {
	ID          string             `json:"id,omitempty"`
	Label       string             `json:"label"`
	Username    string             `json:"username"`
	Password    *string            `json:"password"`
	Platform    model.Platform     `json:"platform"`
	Concurrency int                `json:"concurrency"`
	Regions     []string           `json:"regions"`
	State       model.AccountState `json:"state"`
	ExpiresAt   string             `json:"expiresAt,omitempty"`
	LastLoginAt string             `json:"lastLoginAt,omitempty"`
	CreatedAt   string             `json:"createdAt,omitempty"`
	Device      *DeviceIdentity    `json:"device,omitempty"`
	Warnings    []WarningCode      `json:"warnings,omitempty"`

	// parseIssue is set by the CSV reader for a row that could not be parsed
	// structurally (wrong field count, invalid integer). It is not part of the
	// wire contract.
	parseIssue *RowIssue
}

// EncryptedKDF, EncryptedCipher and EncryptedPlaintext are the self-describing
// envelope header. Every field except ciphertext is authenticated as AAD.
type EncryptedKDF struct {
	Name       string `json:"name"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
}

type EncryptedCipher struct {
	Name  string `json:"name"`
	Nonce string `json:"nonce"`
}

type EncryptedPlaintext struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
}

// EncryptedExport is the .ajsx envelope.
type EncryptedExport struct {
	Format     string             `json:"format"`
	Version    int                `json:"version"`
	CreatedAt  string             `json:"createdAt"`
	KDF        EncryptedKDF       `json:"kdf"`
	Cipher     EncryptedCipher    `json:"cipher"`
	Plaintext  EncryptedPlaintext `json:"plaintext"`
	Ciphertext string             `json:"ciphertext"`
}

// ExportAccount is one account copied out of the store together with its
// plaintext password (when available) and device identity.
type ExportAccount struct {
	Account     model.Account
	Password    []byte
	HasPassword bool
	Device      *DeviceIdentity

	sealed []byte
}

// ExportQuery selects accounts for export. IDs take precedence; a non-empty
// filter applies only when IDs is empty; neither selects every account.
type ExportQuery struct {
	IDs      []string
	State    string
	Platform string
	Q        string
}

// ExportOptions controls document construction.
type ExportOptions struct {
	IncludeDevice bool
	SecretsSealed bool
	DaemonVersion string
	Now           time.Time
}

// AccountImportOptions is the normalised option block from the import
// wrapper. The HTTP layer validates it before planning.
type AccountImportOptions struct {
	Mode               string `json:"mode,omitempty"`
	MatchBy            string `json:"matchBy,omitempty"`
	OnConflict         string `json:"onConflict,omitempty"`
	InitialState       string `json:"initialState,omitempty"`
	IDPolicy           string `json:"idPolicy,omitempty"`
	DevicePolicy       string `json:"devicePolicy,omitempty"`
	PreserveTimestamps *bool  `json:"preserveTimestamps,omitempty"`
	DryRun             bool   `json:"dryRun,omitempty"`
}

// AccountImportSource is the strict wrapper's source block.
type AccountImportSource struct {
	Kind       string          `json:"kind"`
	Content    json.RawMessage `json:"content"`
	Passphrase string          `json:"passphrase,omitempty"`
}

// AccountImportRequest is the strict POST /v1/accounts/import body.
type AccountImportRequest struct {
	Source  AccountImportSource  `json:"source"`
	Options AccountImportOptions `json:"options"`
}

// AccountImportRowResult is one row of the import response. Code and Message
// are set only for a failed row and never carry a credential.
type AccountImportRowResult struct {
	Row        int           `json:"row"`
	Action     string        `json:"action"`
	ID         *string       `json:"id"`
	Label      string        `json:"label"`
	ExistingID *string       `json:"existingId"`
	RenamedTo  *string       `json:"renamedTo"`
	Warnings   []WarningCode `json:"warnings,omitempty"`
	Code       *string       `json:"code"`
	Message    *string       `json:"message"`
}

// AccountImportCounts satisfies created+updated+skipped+failed == total.
// Renamed is an informational subset of created.
type AccountImportCounts struct {
	Total   int `json:"total"`
	Created int `json:"created"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
	Renamed int `json:"renamed"`
	Failed  int `json:"failed"`
}

// AccountImportResult is the import response for both dry-run and apply.
type AccountImportResult struct {
	Format   string                   `json:"format"`
	Version  int                      `json:"version"`
	DryRun   bool                     `json:"dryRun"`
	Applied  bool                     `json:"applied"`
	Mode     string                   `json:"mode"`
	Counts   AccountImportCounts      `json:"counts"`
	Results  []AccountImportRowResult `json:"results"`
	Warnings []WarningCode            `json:"warnings,omitempty"`
}

// ImportCreate is one planned create. An empty ID means the store assigns the
// next acc-NN; a non-empty ID is preserved and bumps the sequence.
type ImportCreate struct {
	ID          string
	Label       string
	Username    string
	Password    []byte
	Platform    model.Platform
	Concurrency int
	Regions     []string
	State       model.AccountState
	ExpiresAt   string
	CreatedAt   string
	LastLoginAt string
	Device      *DeviceIdentity
}

// ImportUpdate is one planned update. A nil Password keeps the current secret.
type ImportUpdate struct {
	ID            string
	Patch         model.AccountPatch
	Password      []byte
	Device        *DeviceIdentity
	ReplaceDevice bool
}

// ImportPlan is the store-side mutation set produced by PlanImport.
type ImportPlan struct {
	Creates []ImportCreate
	Updates []ImportUpdate
	Format  string
	Mode    string
	Skipped int
	Renamed int
	Failed  int
}

func (p ImportPlan) totalRows() int {
	return len(p.Creates) + len(p.Updates) + p.Skipped + p.Failed
}

// ImportApply reports the accounts a successful apply created and updated.
type ImportApply struct {
	Created []model.Account
	Updated []model.Account
}

// BulkDeleteRequest is the strict POST /v1/accounts/bulk-delete body.
type BulkDeleteRequest struct {
	IDs    []string `json:"ids"`
	Mode   string   `json:"mode,omitempty"`
	DryRun bool     `json:"dryRun,omitempty"`
}

// BulkDeleteCounts satisfies total = deleted + wouldDelete + notFound +
// failed + skipped.
type BulkDeleteCounts struct {
	Total       int `json:"total"`
	Deleted     int `json:"deleted"`
	WouldDelete int `json:"wouldDelete"`
	NotFound    int `json:"notFound"`
	Failed      int `json:"failed"`
	Skipped     int `json:"skipped"`
}

// BulkDeleteItem is one per-id delete result.
type BulkDeleteItem struct {
	ID       string        `json:"id"`
	Label    *string       `json:"label"`
	Status   string        `json:"status"`
	Nodes    int           `json:"nodes"`
	Leases   int           `json:"leases"`
	Code     *string       `json:"code"`
	Message  *string       `json:"message"`
	Warnings []WarningCode `json:"warnings,omitempty"`
}

// BulkDeleteResult is the bulk-delete response.
type BulkDeleteResult struct {
	Format  string           `json:"format"`
	Version int              `json:"version"`
	DryRun  bool             `json:"dryRun"`
	Mode    string           `json:"mode"`
	Counts  BulkDeleteCounts `json:"counts"`
	Results []BulkDeleteItem `json:"results"`
}

// AccountIOCapabilities is the GET /v1/accounts/io/capabilities response.
type AccountIOCapabilities struct {
	EncryptedExport    bool  `json:"encryptedExport"`
	PlaintextExport    bool  `json:"plaintextExport"`
	MaxImportRows      int   `json:"maxImportRows"`
	MaxExportAccounts  int   `json:"maxExportAccounts"`
	MaxDeleteIDs       int   `json:"maxDeleteIDs"`
	MaxBodyBytes       int64 `json:"maxBodyBytes"`
	MaxImportBodyBytes int64 `json:"maxImportBodyBytes"`
}

// BulkSealer is implemented by *secret.Store. A sealer without it falls back
// to one Seal per entry, which is correct but rewrites the file per password.
type BulkSealer interface {
	SealMany(entries map[string][]byte) (map[string][]byte, error)
}

// SecretRestorer restores exact sealed envelopes for import rollback.
type SecretRestorer interface {
	RestoreMany(entries map[string][]byte) error
}

// SecretRemover removes sealed envelopes for deleted or rolled-back ids.
type SecretRemover interface {
	RemoveMany(accountIDs []string) error
}

var accountIDPattern = regexp.MustCompile(`^acc-[0-9]{2,}$`)

// SecretsEnabled reports whether the store can seal passwords.
func (s *Store) SecretsEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sealer != nil
}

// RecordAudit appends one audit event. It exists so the HTTP layer can record
// export and bulk-delete summaries without exposing the audit slice.
func (s *Store) RecordAudit(severity model.Severity, actor, action, target, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addAuditLocked(severity, actor, action, target, detail)
}

// AccountDeleteStats returns the label and the node/lease counts of one
// account without mutating anything. The bulk-delete dry run uses it.
func (s *Store) AccountDeleteStats(id string) (label string, nodes, leases int, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.accountIndexLocked(id)
	if idx < 0 {
		return "", 0, 0, false
	}
	for _, node := range s.nodes {
		if node.AccountID == id {
			nodes++
		}
	}
	for _, lease := range s.leases {
		if lease.AccountID == id {
			leases++
		}
	}
	return s.accounts[idx].Label, nodes, leases, true
}

// ExportAccounts copies the selected accounts, sealed blobs and device
// identities under one read lock and opens the passwords outside it. A missing
// id fails the whole selection; a corrupt blob returns ErrConflict rather than
// exporting incomplete credentials.
func (s *Store) ExportAccounts(query ExportQuery) ([]ExportAccount, error) {
	if query.State != "" && !validAccountState(model.AccountState(query.State)) {
		return nil, invalidf("state must be one of active, paused, auth_failed, expired")
	}
	if query.Platform != "" && !validPlatform(model.Platform(query.Platform)) {
		return nil, invalidf("platform must be one of android, ios, windows, linux")
	}
	q := strings.ToLower(strings.TrimSpace(query.Q))

	s.mu.RLock()
	selected := make([]ExportAccount, 0, len(query.IDs))
	if len(query.IDs) > 0 {
		seen := make(map[string]struct{}, len(query.IDs))
		for _, id := range query.IDs {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			idx := s.accountIndexLocked(id)
			if idx < 0 {
				s.mu.RUnlock()
				return nil, notFoundf("account %q not found", id)
			}
			selected = append(selected, s.exportAccountLocked(s.accounts[idx]))
		}
	} else {
		for _, account := range s.accounts {
			if query.State != "" && string(account.State) != query.State {
				continue
			}
			if query.Platform != "" && string(account.Platform) != query.Platform {
				continue
			}
			if q != "" && !accountMatchesQuery(account, q) {
				continue
			}
			selected = append(selected, s.exportAccountLocked(account))
		}
	}
	sealer := s.sealer
	s.mu.RUnlock()

	if len(selected) > MaxExportAccounts {
		return nil, invalidf("at most %d accounts can be exported at once; select or filter the set", MaxExportAccounts)
	}
	for i := range selected {
		if len(selected[i].sealed) == 0 {
			continue
		}
		if sealer == nil {
			selected[i].sealed = nil
			continue
		}
		plaintext, err := sealer.Open(selected[i].Account.ID, selected[i].sealed)
		if err != nil {
			return nil, conflictf("account secret cannot be opened")
		}
		selected[i].Password = plaintext
		selected[i].HasPassword = true
		selected[i].sealed = nil
	}
	return selected, nil
}

func (s *Store) exportAccountLocked(account model.Account) ExportAccount {
	out := ExportAccount{Account: cloneAccount(account)}
	if sealed, ok := s.secrets[account.ID]; ok {
		out.sealed = append([]byte(nil), sealed...)
	}
	if device, ok := s.devices[account.ID]; ok {
		copied := device
		out.Device = &copied
	}
	return out
}

func accountMatchesQuery(account model.Account, lowerQuery string) bool {
	if strings.Contains(strings.ToLower(account.Label), lowerQuery) ||
		strings.Contains(strings.ToLower(account.Username), lowerQuery) {
		return true
	}
	for _, region := range account.Regions {
		if strings.Contains(strings.ToLower(region), lowerQuery) {
			return true
		}
	}
	return false
}

// BuildExport turns exported accounts into the plaintext document. It is the
// only place that copies a plaintext password into a wire row; the caller
// clears the []byte copies afterwards.
func BuildExport(rows []ExportAccount, opts ExportOptions) AccountExport {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	doc := AccountExport{
		Format:     AccountExportFormat,
		Version:    AccountExportVersion,
		ExportedAt: now.UTC().Format(time.RFC3339),
		Source: AccountExportSource{
			DaemonVersion: opts.DaemonVersion,
			SecretsSealed: opts.SecretsSealed,
		},
		Accounts: make([]AccountExportRow, 0, len(rows)),
	}
	for _, row := range rows {
		out := AccountExportRow{
			ID:          row.Account.ID,
			Label:       row.Account.Label,
			Username:    row.Account.Username,
			Platform:    row.Account.Platform,
			Concurrency: row.Account.Concurrency,
			Regions:     append([]string{}, row.Account.Regions...),
			State:       row.Account.State,
			ExpiresAt:   row.Account.ExpiresAt,
			LastLoginAt: row.Account.LastLoginAt,
			CreatedAt:   row.Account.CreatedAt,
		}
		if row.HasPassword {
			password := string(row.Password)
			out.Password = &password
			doc.Counts.Passwords++
		} else {
			out.Warnings = append(out.Warnings, WarningPasswordUnavailable)
		}
		if opts.IncludeDevice && row.Device != nil {
			device := *row.Device
			out.Device = &device
			doc.Counts.Devices++
		}
		doc.Accounts = append(doc.Accounts, out)
	}
	doc.Counts.Accounts = len(doc.Accounts)
	return doc
}

// ---
// Validation and merge planning.

// Normalized fills the documented defaults. It never mutates the receiver.
func (o AccountImportOptions) Normalized() AccountImportOptions {
	if o.Mode == "" {
		o.Mode = "atomic"
	}
	if o.MatchBy == "" {
		o.MatchBy = "label"
	}
	if o.OnConflict == "" {
		o.OnConflict = "skip"
	}
	if o.InitialState == "" {
		o.InitialState = "paused"
	}
	if o.IDPolicy == "" {
		o.IDPolicy = "new"
	}
	if o.DevicePolicy == "" {
		o.DevicePolicy = "restore_if_missing"
	}
	return o
}

// Validate checks the option block before any file is parsed.
func (o AccountImportOptions) Validate() error {
	switch o.Mode {
	case "atomic", "partial":
	default:
		return invalidf("mode must be atomic or partial")
	}
	switch o.MatchBy {
	case "label", "username_platform", "id":
	default:
		return invalidf("matchBy must be label, username_platform or id")
	}
	switch o.OnConflict {
	case "skip", "update", "rename", "fail":
	default:
		return invalidf("onConflict must be skip, update, rename or fail")
	}
	switch o.InitialState {
	case "paused", "active", "from_file":
	default:
		return invalidf("initialState must be paused, active or from_file")
	}
	switch o.IDPolicy {
	case "new", "preserve":
	default:
		return invalidf("idPolicy must be new or preserve")
	}
	switch o.DevicePolicy {
	case "keep", "restore_if_missing", "replace":
	default:
		return invalidf("devicePolicy must be keep, restore_if_missing or replace")
	}
	if o.OnConflict == "rename" && o.IDPolicy == "preserve" {
		return invalidf("rename requires idPolicy new")
	}
	return nil
}

func (o AccountImportOptions) preserveTimestamps() bool {
	if o.PreserveTimestamps == nil {
		return true
	}
	return *o.PreserveTimestamps
}

func issuef(code, format string, args ...any) *RowIssue {
	return &RowIssue{Code: code, Message: fmt.Sprintf(format, args...)}
}

// validateExportRow validates every field that does not depend on whether the
// row becomes a create or an update. A create additionally requires a
// password; the planner adds that check after matching.
func validateExportRow(row AccountExportRow) *RowIssue {
	if row.parseIssue != nil {
		return row.parseIssue
	}
	if row.ID != "" && !accountIDPattern.MatchString(row.ID) {
		return issuef(CodeInvalidArgument, "id %q must match acc-NN", row.ID)
	}
	label := strings.TrimSpace(row.Label)
	switch {
	case label == "":
		return issuef(CodeInvalidArgument, "label is required")
	case len(label) > MaxLabelBytes:
		return issuef(CodeInvalidArgument, "label must be at most %d bytes", MaxLabelBytes)
	case hasControl(label):
		return issuef(CodeInvalidArgument, "label must not contain control characters")
	}
	username := strings.TrimSpace(row.Username)
	switch {
	case username == "":
		return issuef(CodeInvalidArgument, "username is required")
	case len(username) > MaxUsernameBytes:
		return issuef(CodeInvalidArgument, "username must be at most %d bytes", MaxUsernameBytes)
	case hasControl(username):
		return issuef(CodeInvalidArgument, "username must not contain control characters")
	}
	if !validPlatform(row.Platform) {
		return issuef(CodeInvalidArgument, "platform must be one of android, ios, windows, linux")
	}
	if row.Password != nil {
		password := *row.Password
		switch {
		case len(password) > MaxPasswordBytes:
			return issuef(CodeInvalidArgument, "password must be at most %d bytes", MaxPasswordBytes)
		case strings.ContainsRune(password, 0):
			return issuef(CodeInvalidArgument, "password must not contain NUL")
		case !utf8.ValidString(password):
			return issuef(CodeInvalidArgument, "password must be valid UTF-8")
		case len(password) > 0 && strings.TrimSpace(password) == "":
			return issuef(CodeInvalidArgument, "password must contain at least one non-whitespace character")
		}
	}
	if row.Concurrency < 0 || row.Concurrency > 2000 {
		return issuef(CodeInvalidArgument, "concurrency must be between 1 and 2000")
	}
	if len(row.Regions) > MaxRegionCount {
		return issuef(CodeInvalidArgument, "regions must contain at most %d entries", MaxRegionCount)
	}
	for _, raw := range row.Regions {
		region := strings.TrimSpace(raw)
		switch {
		case region == "":
			return issuef(CodeInvalidArgument, "regions must not contain empty values")
		case len(region) > MaxRegionBytes:
			return issuef(CodeInvalidArgument, "region %q is too long", region)
		case hasControl(region):
			return issuef(CodeInvalidArgument, "region %q must not contain control characters", region)
		}
	}
	if row.State != "" && !validAccountState(row.State) {
		return issuef(CodeInvalidArgument, "state must be one of active, paused, auth_failed, expired")
	}
	if row.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, row.ExpiresAt); err != nil {
			return issuef(CodeInvalidArgument, "expiresAt must be an RFC 3339 timestamp")
		}
	}
	if row.CreatedAt != "" {
		if _, err := time.Parse(time.RFC3339, row.CreatedAt); err != nil {
			return issuef(CodeInvalidArgument, "createdAt must be an RFC 3339 timestamp")
		}
	}
	if row.LastLoginAt != "" {
		if _, err := time.Parse(time.RFC3339, row.LastLoginAt); err != nil {
			return issuef(CodeInvalidArgument, "lastLoginAt must be an RFC 3339 timestamp")
		}
	}
	if row.Device != nil {
		if issue := validateDevice(*row.Device); issue != nil {
			return issue
		}
	}
	return nil
}

func validateDevice(dev DeviceIdentity) *RowIssue {
	required := []struct {
		name  string
		value string
	}{
		{"uid", dev.UID},
		{"osdid", dev.OSDID},
		{"cuid", dev.CUID},
		{"cseid", dev.CSEID},
	}
	for _, field := range required {
		value := strings.TrimSpace(field.value)
		switch {
		case value == "":
			return issuef(CodeInvalidArgument, "device %s is required", field.name)
		case len(value) > 128:
			return issuef(CodeInvalidArgument, "device %s must be at most 128 bytes", field.name)
		case hasControl(value):
			return issuef(CodeInvalidArgument, "device %s must not contain control characters", field.name)
		}
	}
	optional := []struct {
		name  string
		value string
	}{
		{"osp", dev.OSP},
		{"osd", dev.OSD},
		{"osv", dev.OSV},
		{"dim", dev.DIM},
		{"version", dev.Version},
		{"site", dev.Site},
		{"hw", dev.HW},
		{"osLang", dev.OSLang},
		{"lang", dev.Lang},
		{"instMkt", dev.InstMkt},
		{"vrf", dev.Vrf},
	}
	for _, field := range optional {
		if len(field.value) > 64 {
			return issuef(CodeInvalidArgument, "device %s must be at most 64 bytes", field.name)
		}
		if hasControl(field.value) {
			return issuef(CodeInvalidArgument, "device %s must not contain control characters", field.name)
		}
	}
	if dev.TZMin < -1440 || dev.TZMin > 1440 {
		return issuef(CodeInvalidArgument, "device tzMin must be between -1440 and 1440")
	}
	return nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func failResult(result *AccountImportRowResult, issue *RowIssue) {
	result.Action = "fail"
	code := issue.Code
	message := issue.Message
	result.Code = &code
	result.Message = &message
}

// PlanImport implements the merge matrix. It never mutates the store; the
// caller applies the returned plan only after checking for failed rows in
// atomic mode.
func PlanImport(doc AccountExport, opts AccountImportOptions, existing []model.Account, secretsEnabled bool) (ImportPlan, []AccountImportRowResult, []WarningCode) {
	opts = opts.Normalized()
	plan := ImportPlan{Format: AccountExportFormat}
	results := make([]AccountImportRowResult, 0, len(doc.Accounts))
	var warnings []WarningCode
	if doc.Counts.Accounts != 0 && doc.Counts.Accounts != len(doc.Accounts) {
		warnings = append(warnings, WarningCountsMismatch)
	}

	labelIndex := make(map[string][]model.Account, len(existing))
	userPlatformIndex := make(map[string][]model.Account, len(existing))
	idIndex := make(map[string]model.Account, len(existing))
	takenLabels := make(map[string]struct{}, len(existing))
	for _, account := range existing {
		labelKey := strings.ToLower(strings.TrimSpace(account.Label))
		labelIndex[labelKey] = append(labelIndex[labelKey], account)
		userPlatformIndex[userPlatformKey(account.Username, account.Platform)] = append(
			userPlatformIndex[userPlatformKey(account.Username, account.Platform)], account)
		idIndex[account.ID] = account
		takenLabels[labelKey] = struct{}{}
	}

	seenLabels := make(map[string]struct{}, len(doc.Accounts))
	seenIDs := make(map[string]struct{}, len(doc.Accounts))
	seenUserPlatform := make(map[string]struct{}, len(doc.Accounts))

	for i, row := range doc.Accounts {
		result := AccountImportRowResult{Row: i + 1, Label: strings.TrimSpace(row.Label)}
		if issue := validateExportRow(row); issue != nil {
			failResult(&result, issue)
			results = append(results, result)
			continue
		}
		labelKey := strings.ToLower(strings.TrimSpace(row.Label))
		if _, duplicate := seenLabels[labelKey]; duplicate {
			failResult(&result, issuef(CodeDuplicateRow, "row %d duplicates an earlier label", i+1))
			results = append(results, result)
			continue
		}
		seenLabels[labelKey] = struct{}{}
		if row.ID != "" {
			if _, duplicate := seenIDs[row.ID]; duplicate {
				failResult(&result, issuef(CodeDuplicateRow, "row %d duplicates an earlier id", i+1))
				results = append(results, result)
				continue
			}
			seenIDs[row.ID] = struct{}{}
		}
		userPlatform := userPlatformKey(strings.TrimSpace(row.Username), row.Platform)
		if _, duplicate := seenUserPlatform[userPlatform]; duplicate {
			failResult(&result, issuef(CodeDuplicateRow, "row %d duplicates an earlier username and platform", i+1))
			results = append(results, result)
			continue
		}
		seenUserPlatform[userPlatform] = struct{}{}

		if opts.MatchBy == "id" && row.ID == "" {
			failResult(&result, issuef(CodeInvalidArgument, "matchBy id requires every row to carry an id"))
			results = append(results, result)
			continue
		}
		matches := matchAccounts(row, opts.MatchBy, labelIndex, userPlatformIndex, idIndex)
		switch {
		case len(matches) == 0:
			create, rowWarnings, issue := planCreateRow(row, opts, strings.TrimSpace(row.Label), secretsEnabled)
			if issue != nil {
				failResult(&result, issue)
				results = append(results, result)
				continue
			}
			if create.ID != "" {
				if _, taken := idIndex[create.ID]; taken {
					failResult(&result, issuef(CodeConflict, "account id %q already exists", create.ID))
					results = append(results, result)
					continue
				}
			}
			plan.Creates = append(plan.Creates, create)
			result.Action = "create"
			if create.ID != "" {
				result.ID = new(create.ID)
			}
			result.Warnings = rowWarnings
			takenLabels[strings.ToLower(create.Label)] = struct{}{}
			results = append(results, result)
		case len(matches) > 1:
			failResult(&result, issuef(CodeConflict, "multiple accounts match this row"))
			results = append(results, result)
		default:
			match := matches[0]
			if opts.IDPolicy == "preserve" && row.ID != "" && row.ID != match.ID {
				failResult(&result, issuef(CodeConflict, "account id %q already belongs to another account", row.ID))
				results = append(results, result)
				continue
			}
			result.ExistingID = new(match.ID)
			switch opts.OnConflict {
			case "skip":
				result.Action = "skip"
				result.ID = new(match.ID)
			case "fail":
				failResult(&result, issuef(CodeConflict, "an account named %q already exists", match.Label))
			case "rename":
				label, ok := renameLabel(strings.TrimSpace(row.Label), func(candidate string) bool {
					_, taken := takenLabels[strings.ToLower(candidate)]
					return taken
				})
				if !ok {
					failResult(&result, issuef(CodeConflict, "could not find a free imported label"))
					results = append(results, result)
					continue
				}
				create, rowWarnings, issue := planCreateRow(row, opts, label, secretsEnabled)
				if issue != nil {
					failResult(&result, issue)
					results = append(results, result)
					continue
				}
				plan.Creates = append(plan.Creates, create)
				result.Action = "rename"
				result.RenamedTo = new(label)
				result.Warnings = rowWarnings
				takenLabels[strings.ToLower(label)] = struct{}{}
			case "update":
				newLabel := strings.ToLower(strings.TrimSpace(row.Label))
				if !strings.EqualFold(strings.TrimSpace(row.Label), match.Label) {
					if _, taken := takenLabels[newLabel]; taken {
						failResult(&result, issuef(CodeConflict, "an account named %q already exists", strings.TrimSpace(row.Label)))
						results = append(results, result)
						continue
					}
				}
				update, rowWarnings, issue := planUpdateRow(row, opts, match, secretsEnabled)
				if issue != nil {
					failResult(&result, issue)
					results = append(results, result)
					continue
				}
				plan.Updates = append(plan.Updates, update)
				result.Action = "update"
				result.ID = new(match.ID)
				result.Warnings = rowWarnings
				if update.Patch.Label != nil {
					takenLabels[strings.ToLower(*update.Patch.Label)] = struct{}{}
				}
			}
			results = append(results, result)
		}
	}
	return plan, results, warnings
}

func userPlatformKey(username string, platform model.Platform) string {
	return username + "\x00" + string(platform)
}

func matchAccounts(row AccountExportRow, matchBy string, labels, userPlatforms map[string][]model.Account, ids map[string]model.Account) []model.Account {
	switch matchBy {
	case "username_platform":
		return userPlatforms[userPlatformKey(strings.TrimSpace(row.Username), row.Platform)]
	case "id":
		if row.ID == "" {
			return nil
		}
		if account, ok := ids[row.ID]; ok {
			return []model.Account{account}
		}
		return nil
	default:
		return labels[strings.ToLower(strings.TrimSpace(row.Label))]
	}
}

func planCreateRow(row AccountExportRow, opts AccountImportOptions, label string, secretsEnabled bool) (ImportCreate, []WarningCode, *RowIssue) {
	if row.Password == nil || *row.Password == "" {
		return ImportCreate{}, nil, issuef(CodePasswordRequired, "a password is required to create an account")
	}
	if !secretsEnabled {
		return ImportCreate{}, nil, issuef(CodeSecretStorageOff, "secret storage is disabled; set AJS_MASTER_KEY")
	}
	state := model.AccountPaused
	switch opts.InitialState {
	case "active":
		state = model.AccountActive
	case "from_file":
		if row.State != "" {
			state = row.State
		}
	}
	concurrency := row.Concurrency
	if concurrency == 0 {
		concurrency = 1
	}
	regions := []string{}
	if row.Regions != nil {
		normalized, err := normalizeRegions(row.Regions)
		if err != nil {
			return ImportCreate{}, nil, issuef(CodeInvalidArgument, "%s", err.Error())
		}
		regions = normalized
	}
	create := ImportCreate{
		Label:       label,
		Username:    strings.TrimSpace(row.Username),
		Password:    []byte(*row.Password),
		Platform:    row.Platform,
		Concurrency: concurrency,
		Regions:     regions,
		State:       state,
		ExpiresAt:   normalizeImportTimestamp(row.ExpiresAt),
	}
	var warnings []WarningCode
	if opts.preserveTimestamps() {
		create.CreatedAt = normalizeImportTimestamp(row.CreatedAt)
		create.LastLoginAt = normalizeImportTimestamp(row.LastLoginAt)
	}
	if opts.IDPolicy == "preserve" {
		if row.ID != "" {
			create.ID = row.ID
		} else {
			warnings = append(warnings, WarningIDIgnored)
		}
	} else if row.ID != "" {
		warnings = append(warnings, WarningIDIgnored)
	}
	if opts.DevicePolicy != "keep" {
		if row.Device != nil {
			device := *row.Device
			create.Device = &device
		} else {
			warnings = append(warnings, WarningDeviceGeneratedOnLogin)
		}
	}
	return create, warnings, nil
}

func planUpdateRow(row AccountExportRow, opts AccountImportOptions, match model.Account, secretsEnabled bool) (ImportUpdate, []WarningCode, *RowIssue) {
	label := strings.TrimSpace(row.Label)
	username := strings.TrimSpace(row.Username)
	patch := model.AccountPatch{
		Label:    &label,
		Username: &username,
		Platform: &row.Platform,
	}
	if row.Concurrency > 0 {
		concurrency := row.Concurrency
		patch.Concurrency = &concurrency
	}
	if row.Regions != nil {
		regions, err := normalizeRegions(row.Regions)
		if err != nil {
			return ImportUpdate{}, nil, issuef(CodeInvalidArgument, "%s", err.Error())
		}
		patch.Regions = &regions
	}
	if opts.InitialState == "from_file" && row.State != "" {
		state := row.State
		patch.State = &state
	}
	if row.ExpiresAt != "" {
		expiresAt := normalizeImportTimestamp(row.ExpiresAt)
		patch.ExpiresAt = &expiresAt
	}
	update := ImportUpdate{ID: match.ID, Patch: patch}
	var warnings []WarningCode
	if row.Password != nil && *row.Password != "" {
		if !secretsEnabled {
			return ImportUpdate{}, nil, issuef(CodeSecretStorageOff, "secret storage is disabled; set AJS_MASTER_KEY")
		}
		update.Password = []byte(*row.Password)
	} else {
		warnings = append(warnings, WarningPasswordKept)
	}
	switch opts.DevicePolicy {
	case "replace":
		if match.State != model.AccountPaused || match.LiveNodeCount != 0 {
			return ImportUpdate{}, nil, issuef(CodeDeviceInUse, "device cannot be replaced while the account is not paused or has live nodes")
		}
		if row.Device != nil {
			device := *row.Device
			update.Device = &device
			update.ReplaceDevice = true
			warnings = append(warnings, WarningDeviceReplaced)
		} else {
			warnings = append(warnings, WarningDeviceKept)
		}
	case "restore_if_missing":
		if row.Device != nil {
			warnings = append(warnings, WarningDeviceKept)
		}
	}
	if row.Device != nil && !update.ReplaceDevice && match.Platform != row.Platform {
		warnings = append(warnings, WarningPlatformDeviceMismatch)
	}
	return update, warnings, nil
}

// renameLabel appends " (imported)" and a numeric suffix on collision,
// truncating on a UTF-8 boundary so the result stays within 80 bytes.
func renameLabel(base string, taken func(string) bool) (string, bool) {
	suffix := " (imported)"
	for attempt := 1; attempt <= 1000; attempt++ {
		candidate := truncateUTF8(base, MaxLabelBytes-len(suffix)) + suffix
		if !taken(candidate) {
			return candidate, true
		}
		suffix = fmt.Sprintf(" (imported %d)", attempt+1)
	}
	return "", false
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.RuneStart(value[maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}

// normalizeImportTimestamp parses a validated RFC 3339 value and stores it in
// UTC. Validation runs before planning, so an unparsable value here is left
// untouched and will be rejected by the store.
func normalizeImportTimestamp(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return iso(parsed)
}

// ---
// Encrypted envelope and plaintext parsers.

// errEncryptedExport is deliberately generic: a wrong passphrase, a tampered
// header, malformed base64 and an unsupported version are indistinguishable to
// the caller.
var errEncryptedExport = errors.New("encrypted export could not be opened")

func validatePassphrase(passphrase string) error {
	switch {
	case len(passphrase) < MinPassphraseBytes:
		return fmt.Errorf("passphrase must be at least %d bytes", MinPassphraseBytes)
	case len(passphrase) > MaxPassphraseBytes:
		return fmt.Errorf("passphrase must be at most %d bytes", MaxPassphraseBytes)
	case strings.ContainsRune(passphrase, 0):
		return errors.New("passphrase must not contain NUL")
	}
	return nil
}

func exportAAD(createdAt string, iterations int, salt, nonce string) []byte {
	return []byte(fmt.Sprintf(
		"%s|%d|%s|PBKDF2-HMAC-SHA256|%d|%s|AES-256-GCM|%s|%s|%d",
		EncryptedExportFormat, 1, createdAt, iterations, salt, nonce,
		AccountExportFormat, AccountExportVersion,
	))
}

// SealExport encrypts the plaintext document with PBKDF2-HMAC-SHA256 and
// AES-256-GCM. The full header is authenticated as AAD. random may be nil, in
// which case crypto/rand is used; tests inject a deterministic reader.
func SealExport(doc AccountExport, passphrase string, random io.Reader) (EncryptedExport, error) {
	if err := validatePassphrase(passphrase); err != nil {
		return EncryptedExport{}, err
	}
	if random == nil {
		random = rand.Reader
	}
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(random, salt); err != nil {
		return EncryptedExport{}, fmt.Errorf("export salt: %w", err)
	}
	if _, err := io.ReadFull(random, nonce); err != nil {
		return EncryptedExport{}, fmt.Errorf("export nonce: %w", err)
	}
	createdAt := time.Now().UTC().Format(time.RFC3339)
	saltB64 := base64.StdEncoding.EncodeToString(salt)
	nonceB64 := base64.StdEncoding.EncodeToString(nonce)

	doc.Format = AccountExportFormat
	doc.Version = AccountExportVersion
	plaintext, err := json.Marshal(doc)
	if err != nil {
		return EncryptedExport{}, fmt.Errorf("encode export: %w", err)
	}
	defer clear(plaintext)
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, ExportPBKDF2Iterations, 32)
	if err != nil {
		return EncryptedExport{}, fmt.Errorf("derive export key: %w", err)
	}
	defer clear(key)
	aead, err := newExportAEAD(key)
	if err != nil {
		return EncryptedExport{}, err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, exportAAD(createdAt, ExportPBKDF2Iterations, saltB64, nonceB64))
	return EncryptedExport{
		Format:    EncryptedExportFormat,
		Version:   1,
		CreatedAt: createdAt,
		KDF: EncryptedKDF{
			Name:       "PBKDF2-HMAC-SHA256",
			Iterations: ExportPBKDF2Iterations,
			Salt:       saltB64,
		},
		Cipher: EncryptedCipher{
			Name:  "AES-256-GCM",
			Nonce: nonceB64,
		},
		Plaintext: EncryptedPlaintext{
			Format:  AccountExportFormat,
			Version: AccountExportVersion,
		},
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	}, nil
}

// OpenExport validates and decrypts an encrypted envelope. Every failure is
// the same generic error so an attacker cannot use the response as an oracle.
func OpenExport(envelope EncryptedExport, passphrase string) (AccountExport, error) {
	fail := func() (AccountExport, error) { return AccountExport{}, errEncryptedExport }
	if envelope.Format != EncryptedExportFormat || envelope.Version != 1 {
		return fail()
	}
	if envelope.KDF.Name != "PBKDF2-HMAC-SHA256" ||
		envelope.KDF.Iterations < MinPBKDF2Iterations ||
		envelope.KDF.Iterations > MaxPBKDF2Iterations {
		return fail()
	}
	salt, err := base64.StdEncoding.DecodeString(envelope.KDF.Salt)
	if err != nil || len(salt) != 16 {
		return fail()
	}
	if envelope.Cipher.Name != "AES-256-GCM" {
		return fail()
	}
	nonce, err := base64.StdEncoding.DecodeString(envelope.Cipher.Nonce)
	if err != nil || len(nonce) != 12 {
		return fail()
	}
	if envelope.Plaintext.Format != AccountExportFormat || envelope.Plaintext.Version != AccountExportVersion {
		return fail()
	}
	if validatePassphrase(passphrase) != nil {
		return fail()
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return fail()
	}
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, envelope.KDF.Iterations, 32)
	if err != nil {
		return fail()
	}
	defer clear(key)
	aead, err := newExportAEAD(key)
	if err != nil {
		return fail()
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext,
		exportAAD(envelope.CreatedAt, envelope.KDF.Iterations, envelope.KDF.Salt, envelope.Cipher.Nonce))
	if err != nil {
		return fail()
	}
	defer clear(plaintext)
	if len(plaintext) > MaxDecodedExportBytes {
		return fail()
	}
	var doc AccountExport
	if err := json.Unmarshal(plaintext, &doc); err != nil {
		return fail()
	}
	if doc.Format != AccountExportFormat || doc.Version != AccountExportVersion {
		return fail()
	}
	return doc, nil
}

func newExportAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("export cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("export gcm: %w", err)
	}
	return aead, nil
}

// ParseExportJSON decodes the plaintext document permissively: unknown fields
// are ignored for forward compatibility, but format and version are exact.
func ParseExportJSON(data []byte) (AccountExport, error) {
	if len(data) > MaxDecodedExportBytes {
		return AccountExport{}, invalidf("decoded export must be at most %d bytes", MaxDecodedExportBytes)
	}
	var doc AccountExport
	if err := json.Unmarshal(data, &doc); err != nil {
		return AccountExport{}, invalidf("invalid account export JSON: %s", err.Error())
	}
	if doc.Format != AccountExportFormat {
		return AccountExport{}, invalidf("unsupported export format %q", doc.Format)
	}
	if doc.Version != AccountExportVersion {
		return AccountExport{}, invalidf("unsupported export version %d", doc.Version)
	}
	if len(doc.Accounts) < 1 || len(doc.Accounts) > MaxImportRows {
		return AccountExport{}, invalidf("export must contain between 1 and %d accounts", MaxImportRows)
	}
	return doc, nil
}

// ParseExportCSV decodes the exact RFC 4180 CSV contract. A structural header
// problem fails the file; a bad data row carries a parseIssue so the planner
// reports it as a row failure instead of dropping it silently.
func ParseExportCSV(data []byte) (AccountExport, []WarningCode, error) {
	if len(data) > MaxDecodedExportBytes {
		return AccountExport{}, nil, invalidf("decoded export must be at most %d bytes", MaxDecodedExportBytes)
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = false

	header, err := reader.Read()
	if err != nil {
		return AccountExport{}, nil, invalidf("invalid CSV header: %s", err.Error())
	}
	columns := make(map[string]int, len(header))
	unknown := make([]string, 0)
	for i, name := range header {
		normalized := strings.ToLower(strings.TrimSpace(name))
		if _, duplicate := columns[normalized]; duplicate {
			return AccountExport{}, nil, invalidf("duplicate CSV column %q", normalized)
		}
		columns[normalized] = i
		switch normalized {
		case "label", "username", "password", "platform", "concurrency", "regions", "state", "expiresat":
		default:
			unknown = append(unknown, normalized)
		}
	}
	required := []string{"label", "username", "password", "platform"}
	for _, name := range required {
		if _, ok := columns[name]; !ok {
			return AccountExport{}, nil, invalidf("CSV is missing required column %q", name)
		}
	}
	var warnings []WarningCode
	if len(unknown) > 0 {
		warnings = append(warnings, WarningUnknownColumn)
	}

	doc := AccountExport{Format: AccountExportFormat, Version: AccountExportVersion}
	rowNumber := 0
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return AccountExport{}, nil, invalidf("invalid CSV row %d: %s", rowNumber+1, err.Error())
		}
		rowNumber++
		row := AccountExportRow{}
		if len(record) != len(header) {
			row.parseIssue = issuef(CodeInvalidArgument, "CSV row %d has %d fields, want %d", rowNumber, len(record), len(header))
			if index, ok := columns["label"]; ok && index < len(record) {
				row.Label = record[index]
			}
			doc.Accounts = append(doc.Accounts, row)
			continue
		}
		row.Label = record[columns["label"]]
		row.Username = record[columns["username"]]
		row.Platform = model.Platform(record[columns["platform"]])
		if password := record[columns["password"]]; password != "" {
			value := password
			row.Password = &value
		}
		if index, ok := columns["concurrency"]; ok && strings.TrimSpace(record[index]) != "" {
			value, err := strconv.Atoi(strings.TrimSpace(record[index]))
			if err != nil {
				row.parseIssue = issuef(CodeInvalidArgument, "CSV row %d concurrency must be an integer", rowNumber)
			} else {
				row.Concurrency = value
			}
		}
		if index, ok := columns["regions"]; ok && record[index] != "" {
			row.Regions = strings.Split(record[index], "|")
		}
		if index, ok := columns["state"]; ok {
			row.State = model.AccountState(strings.TrimSpace(record[index]))
		}
		if index, ok := columns["expiresat"]; ok {
			row.ExpiresAt = strings.TrimSpace(record[index])
		}
		doc.Accounts = append(doc.Accounts, row)
	}
	return doc, warnings, nil
}

// BuildExportCSV encodes the exact header and RFC 4180 quoting. Region tags
// containing a vertical bar or a line break cannot round-trip, so the export
// fails instead of silently changing them.
func BuildExportCSV(doc AccountExport) ([]byte, error) {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	header := []string{"label", "username", "password", "platform", "concurrency", "regions", "state", "expiresAt"}
	if err := writer.Write(header); err != nil {
		return nil, fmt.Errorf("encode CSV header: %w", err)
	}
	for _, row := range doc.Accounts {
		for _, region := range row.Regions {
			if strings.ContainsAny(region, "|\r\n") {
				return nil, conflictf("region %q contains a character that CSV cannot round-trip; use JSON or encrypted export", region)
			}
		}
		password := ""
		if row.Password != nil {
			password = *row.Password
		}
		concurrency := row.Concurrency
		if concurrency == 0 {
			concurrency = 1
		}
		record := []string{
			row.Label,
			row.Username,
			password,
			string(row.Platform),
			strconv.Itoa(concurrency),
			strings.Join(row.Regions, "|"),
			string(row.State),
			row.ExpiresAt,
		}
		if err := writer.Write(record); err != nil {
			return nil, fmt.Errorf("encode CSV row: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("encode CSV: %w", err)
	}
	return buffer.Bytes(), nil
}

// ---
// Atomic import apply.

// ImportAccounts applies a plan as one store operation. Passwords are sealed
// outside the write lock, then every account, device and secret change is
// applied under one lock. A failure rolls the affected rows back and restores
// the previous sealed envelopes; accountSeq stays advanced because burned ids
// are safe.
func (s *Store) ImportAccounts(plan ImportPlan, actor ...string) (ImportApply, error) {
	if len(plan.Creates) == 0 && len(plan.Updates) == 0 {
		return ImportApply{}, nil
	}
	hasPasswords := false
	for _, create := range plan.Creates {
		if len(create.Password) == 0 {
			return ImportApply{}, invalidf("a password is required to create an account")
		}
		hasPasswords = true
	}
	for _, update := range plan.Updates {
		if len(update.Password) > 0 {
			hasPasswords = true
		}
	}
	if hasPasswords && !s.SecretsEnabled() {
		return ImportApply{}, invalidf("secret storage is disabled; set AJS_MASTER_KEY")
	}

	type createSlot struct {
		create ImportCreate
		id     string
	}
	slots := make([]createSlot, len(plan.Creates))
	reservedLabels := make([]string, 0, len(plan.Creates)+len(plan.Updates))
	createdIDs := make([]string, 0, len(plan.Creates))
	updatedIDs := make([]string, 0, len(plan.Updates))
	previousAccounts := make(map[string]model.Account, len(plan.Updates))
	previousDevices := make(map[string]DeviceIdentity, len(plan.Updates))
	previousSealed := make(map[string][]byte, len(plan.Updates))
	entries := make(map[string][]byte, len(plan.Creates)+len(plan.Updates))

	releaseLabels := func() {
		if len(reservedLabels) == 0 {
			return
		}
		s.mu.Lock()
		for _, label := range reservedLabels {
			delete(s.pendingLabels, strings.ToLower(label))
		}
		s.mu.Unlock()
	}

	s.mu.Lock()
	plannedIDs := make(map[string]struct{}, len(plan.Creates))
	for i, create := range plan.Creates {
		id := create.ID
		if id != "" {
			if !accountIDPattern.MatchString(id) {
				s.mu.Unlock()
				releaseLabels()
				return ImportApply{}, invalidf("id %q must match acc-NN", id)
			}
			if _, duplicate := plannedIDs[id]; duplicate {
				s.mu.Unlock()
				releaseLabels()
				return ImportApply{}, conflictf("account id %q is duplicated in the plan", id)
			}
			plannedIDs[id] = struct{}{}
			if s.accountIndexLocked(id) >= 0 {
				s.mu.Unlock()
				releaseLabels()
				return ImportApply{}, conflictf("account id %q already exists", id)
			}
			if number, ok := accountIDNumber(id); ok && number > s.accountSeq {
				s.accountSeq = number
			}
		} else {
			id = s.reserveAccountIDLocked()
		}
		label := strings.TrimSpace(create.Label)
		if s.accountLabelTakenLocked(label) {
			s.mu.Unlock()
			releaseLabels()
			return ImportApply{}, conflictf("an account named %q already exists", label)
		}
		s.pendingLabels[strings.ToLower(label)] = struct{}{}
		reservedLabels = append(reservedLabels, label)
		slots[i] = createSlot{create: create, id: id}
		createdIDs = append(createdIDs, id)
		if len(create.Password) > 0 {
			entries[id] = create.Password
		}
	}
	for _, update := range plan.Updates {
		idx := s.accountIndexLocked(update.ID)
		if idx < 0 {
			s.mu.Unlock()
			releaseLabels()
			return ImportApply{}, notFoundf("account %q not found", update.ID)
		}
		if sealed, ok := s.secrets[update.ID]; ok {
			previousSealed[update.ID] = append([]byte(nil), sealed...)
		}
		updatedIDs = append(updatedIDs, update.ID)
		if update.Patch.Label != nil {
			label := strings.TrimSpace(*update.Patch.Label)
			if s.importLabelTakenLocked(label, update.ID) {
				s.mu.Unlock()
				releaseLabels()
				return ImportApply{}, conflictf("an account named %q already exists", label)
			}
			s.pendingLabels[strings.ToLower(label)] = struct{}{}
			reservedLabels = append(reservedLabels, label)
		}
		if len(update.Password) > 0 {
			entries[update.ID] = update.Password
		}
	}
	s.mu.Unlock()

	sealed := make(map[string][]byte, len(entries))
	if len(entries) > 0 {
		if bulk, ok := s.sealer.(BulkSealer); ok {
			var err error
			sealed, err = bulk.SealMany(entries)
			if err != nil {
				releaseLabels()
				return ImportApply{}, fmt.Errorf("seal account secrets: %w", err)
			}
		} else {
			for id, password := range entries {
				envelope, err := s.sealer.Seal(id, password)
				if err != nil {
					releaseLabels()
					removeSealedIDs(s.sealer, keysOf(sealed))
					return ImportApply{}, fmt.Errorf("seal account secrets: %w", err)
				}
				sealed[id] = envelope
			}
		}
	}
	for id := range entries {
		if len(sealed[id]) == 0 {
			releaseLabels()
			restoreSealedState(s.sealer, previousSealed, createdIDs, updatedIDs)
			return ImportApply{}, fmt.Errorf("seal account secrets: missing envelope for %s", id)
		}
	}

	s.mu.Lock()
	previousAudit := append([]model.AuditEvent(nil), s.audit...)
	apply := ImportApply{}
	changedFields := make(map[string][]string, len(plan.Updates))
	var applyErr error
	plannedLabels := make(map[string]struct{}, len(slots))
	for i := range slots {
		label := strings.TrimSpace(slots[i].create.Label)
		key := strings.ToLower(label)
		if _, duplicate := plannedLabels[key]; duplicate {
			applyErr = conflictf("an account named %q already exists", label)
			break
		}
		for _, existing := range s.accounts {
			if strings.EqualFold(existing.Label, label) {
				applyErr = conflictf("an account named %q already exists", label)
				break
			}
		}
		if applyErr != nil {
			break
		}
		plannedLabels[key] = struct{}{}
		account := s.insertImportedAccountLocked(slots[i].create, slots[i].id, sealed[slots[i].id])
		apply.Created = append(apply.Created, account)
	}
	if applyErr == nil {
		for _, update := range plan.Updates {
			if idx := s.accountIndexLocked(update.ID); idx >= 0 {
				previousAccounts[update.ID] = cloneAccount(s.accounts[idx])
				if device, ok := s.devices[update.ID]; ok {
					previousDevices[update.ID] = device
				}
			}
			account, changed, err := s.applyImportUpdateLocked(update, sealed[update.ID])
			if err != nil {
				applyErr = err
				break
			}
			apply.Updated = append(apply.Updated, account)
			changedFields[update.ID] = changed
		}
	}
	if applyErr != nil {
		s.rollbackImportLocked(apply, previousAccounts, previousDevices, previousSealed, previousAudit)
		s.mu.Unlock()
		releaseLabels()
		restoreSealedState(s.sealer, previousSealed, createdIDs, updatedIDs)
		return ImportApply{}, applyErr
	}

	s.syncAccountCountsLocked()
	if len(apply.Created) > 0 || len(apply.Updated) > 0 {
		if plan.totalRows() <= 20 {
			for _, account := range apply.Created {
				s.addAuditLocked(model.SeverityInfo, auditActorArg(actor), "account.create", account.Label,
					fmt.Sprintf("source=import platform=%s concurrency=%d", account.Platform, account.Concurrency))
			}
			for _, account := range apply.Updated {
				fields := strings.Join(changedFields[account.ID], ", ")
				if fields == "" {
					fields = "none"
				}
				s.addAuditLocked(model.SeverityInfo, auditActorArg(actor), "account.update", account.Label,
					"source=import fields: "+fields)
			}
		}
		format := plan.Format
		if format == "" {
			format = "unknown"
		}
		s.addAuditLocked(model.SeverityInfo, auditActorArg(actor), "account.import", "accounts",
			fmt.Sprintf("format=%s mode=%s created=%d updated=%d skipped=%d renamed=%d failed=%d dry_run=false",
				format, plan.Mode, len(apply.Created), len(apply.Updated), plan.Skipped, plan.Renamed, plan.Failed))
	}
	for _, label := range reservedLabels {
		delete(s.pendingLabels, strings.ToLower(label))
	}
	s.mu.Unlock()
	return apply, nil
}

func (s *Store) insertImportedAccountLocked(create ImportCreate, id string, sealed []byte) model.Account {
	now := s.now()
	concurrency := create.Concurrency
	if concurrency == 0 {
		concurrency = 1
	}
	state := create.State
	if state == "" {
		state = model.AccountPaused
	}
	expiresAt := create.ExpiresAt
	if expiresAt == "" {
		expiresAt = iso(now.Add(30 * 24 * time.Hour))
	}
	createdAt := create.CreatedAt
	if createdAt == "" {
		createdAt = iso(now)
	}
	account := model.Account{
		ID:          id,
		Label:       strings.TrimSpace(create.Label),
		Username:    strings.TrimSpace(create.Username),
		Platform:    create.Platform,
		State:       state,
		Concurrency: concurrency,
		Regions:     append([]string{}, create.Regions...),
		ExpiresAt:   expiresAt,
		LastLoginAt: create.LastLoginAt,
		CreatedAt:   createdAt,
	}
	if sealed != nil {
		s.secrets[id] = sealed
	}
	if create.Device != nil {
		s.devices[id] = *create.Device
	}
	s.accounts = append([]model.Account{account}, s.accounts...)
	return cloneAccount(account)
}

func (s *Store) applyImportUpdateLocked(update ImportUpdate, sealed []byte) (model.Account, []string, error) {
	idx := s.accountIndexLocked(update.ID)
	if idx < 0 {
		return model.Account{}, nil, notFoundf("account %q not found", update.ID)
	}
	next := s.accounts[idx]
	changed := make([]string, 0, 8)
	if patch := update.Patch; patch.Label != nil {
		label := strings.TrimSpace(*patch.Label)
		switch {
		case label == "":
			return model.Account{}, nil, invalidf("label is required")
		case len(label) > MaxLabelBytes:
			return model.Account{}, nil, invalidf("label must be at most %d bytes", MaxLabelBytes)
		case s.importAccountLabelTakenLocked(label, update.ID):
			return model.Account{}, nil, conflictf("an account named %q already exists", label)
		}
		if label != next.Label {
			next.Label = label
			changed = append(changed, "label")
			// Labels are denormalised onto nodes and leases.
			for i := range s.nodes {
				if s.nodes[i].AccountID == update.ID {
					s.nodes[i].AccountLabel = label
				}
			}
			for i := range s.leases {
				if s.leases[i].AccountID == update.ID {
					s.leases[i].AccountLabel = label
				}
			}
		}
	}
	if patch := update.Patch; patch.Username != nil {
		username := strings.TrimSpace(*patch.Username)
		if username == "" {
			return model.Account{}, nil, invalidf("username is required")
		}
		if username != next.Username {
			next.Username = username
			changed = append(changed, "username")
		}
	}
	if patch := update.Patch; patch.Platform != nil {
		if !validPlatform(*patch.Platform) {
			return model.Account{}, nil, invalidf("platform must be one of android, ios, windows, linux")
		}
		if *patch.Platform != next.Platform {
			next.Platform = *patch.Platform
			changed = append(changed, "platform")
		}
	}
	if patch := update.Patch; patch.State != nil {
		if !validAccountState(*patch.State) {
			return model.Account{}, nil, invalidf("state must be one of active, paused, auth_failed, expired")
		}
		if *patch.State != next.State {
			next.State = *patch.State
			changed = append(changed, "state")
		}
	}
	if patch := update.Patch; patch.Concurrency != nil {
		if *patch.Concurrency < 1 || *patch.Concurrency > 2000 {
			return model.Account{}, nil, invalidf("concurrency must be between 1 and 2000")
		}
		if *patch.Concurrency != next.Concurrency {
			next.Concurrency = *patch.Concurrency
			changed = append(changed, "concurrency")
		}
	}
	if patch := update.Patch; patch.Regions != nil {
		regions, err := normalizeRegions(*patch.Regions)
		if err != nil {
			return model.Account{}, nil, err
		}
		next.Regions = regions
		changed = append(changed, "regions")
	}
	if patch := update.Patch; patch.ExpiresAt != nil {
		parsed, err := time.Parse(time.RFC3339, *patch.ExpiresAt)
		if err != nil {
			return model.Account{}, nil, invalidf("expiresAt must be an RFC 3339 timestamp")
		}
		next.ExpiresAt = iso(parsed)
		changed = append(changed, "expiresAt")
	}
	if len(sealed) > 0 {
		s.secrets[update.ID] = sealed
		changed = append(changed, "password")
	}
	if update.ReplaceDevice && update.Device != nil {
		s.devices[update.ID] = *update.Device
		changed = append(changed, "device")
	}
	s.accounts[idx] = next
	return cloneAccount(next), changed, nil
}

// rollbackImportLocked restores only the rows this import touched. The caller
// holds the write lock and restores the sealed file outside it.
func (s *Store) rollbackImportLocked(apply ImportApply, previousAccounts map[string]model.Account, previousDevices map[string]DeviceIdentity, previousSealed map[string][]byte, previousAudit []model.AuditEvent) {
	created := make(map[string]struct{}, len(apply.Created))
	for _, account := range apply.Created {
		created[account.ID] = struct{}{}
		delete(s.secrets, account.ID)
		delete(s.devices, account.ID)
	}
	kept := s.accounts[:0]
	for _, account := range s.accounts {
		if _, rollback := created[account.ID]; rollback {
			continue
		}
		kept = append(kept, account)
	}
	s.accounts = kept
	for id, account := range previousAccounts {
		if idx := s.accountIndexLocked(id); idx >= 0 {
			s.accounts[idx] = cloneAccount(account)
		}
		delete(s.secrets, id)
		delete(s.devices, id)
	}
	for id, device := range previousDevices {
		s.devices[id] = device
	}
	for id, sealed := range previousSealed {
		s.secrets[id] = sealed
	}
	s.audit = previousAudit
	s.syncAccountCountsLocked()
}

func (s *Store) importLabelTakenLocked(label, exceptID string) bool {
	for _, account := range s.accounts {
		if account.ID != exceptID && strings.EqualFold(account.Label, label) {
			return true
		}
	}
	if _, pending := s.pendingLabels[strings.ToLower(label)]; pending {
		if exceptID != "" {
			if idx := s.accountIndexLocked(exceptID); idx >= 0 && strings.EqualFold(s.accounts[idx].Label, label) {
				return false
			}
		}
		return true
	}
	return false
}

// importAccountLabelTakenLocked checks committed accounts only. The apply
// phase holds the write lock and has already reserved this import's labels in
// pendingLabels, so a pending hit would be the caller's own reservation.
func (s *Store) importAccountLabelTakenLocked(label, exceptID string) bool {
	for _, account := range s.accounts {
		if account.ID != exceptID && strings.EqualFold(account.Label, label) {
			return true
		}
	}
	return false
}

func accountIDNumber(id string) (int, bool) {
	if !strings.HasPrefix(id, "acc-") {
		return 0, false
	}
	number, err := strconv.Atoi(strings.TrimPrefix(id, "acc-"))
	if err != nil || number < 0 {
		return 0, false
	}
	return number, true
}

func keysOf(entries map[string][]byte) []string {
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	return ids
}

func removeSealedIDs(sealer Sealer, ids []string) {
	if len(ids) == 0 {
		return
	}
	if remover, ok := sealer.(SecretRemover); ok {
		_ = remover.RemoveMany(ids)
		return
	}
	for _, id := range ids {
		_ = sealer.Delete(id)
	}
}

// restoreSealedState puts the secret store back after a failed apply. The
// previous envelopes are restored byte for byte; ids that had no previous
// entry are removed. Errors are best-effort: the account mutation already
// rolled back, and the startup prune retries an orphan later.
func restoreSealedState(sealer Sealer, previous map[string][]byte, createdIDs, updatedIDs []string) {
	if sealer == nil {
		return
	}
	restore := make(map[string][]byte, len(previous))
	for id, sealed := range previous {
		restore[id] = sealed
	}
	if restorer, ok := sealer.(SecretRestorer); ok {
		_ = restorer.RestoreMany(restore)
	}
	remove := append([]string{}, createdIDs...)
	for _, id := range updatedIDs {
		if _, ok := previous[id]; !ok {
			remove = append(remove, id)
		}
	}
	if remover, ok := sealer.(SecretRemover); ok {
		_ = remover.RemoveMany(remove)
		return
	}
	for _, id := range remove {
		_ = sealer.Delete(id)
	}
}
