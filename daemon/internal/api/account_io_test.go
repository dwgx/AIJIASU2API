package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/runtime"
	"ajsaccountd/internal/secret"
	"ajsaccountd/internal/store"
)

const accountIOPassphrase = "correct horse battery staple"

func accountIOKey() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

func newAccountIOServer(t *testing.T, mutate func(*Config)) (*httptest.Server, *Server, *store.Store, *secret.Store, *bytes.Buffer) {
	t.Helper()
	return newAccountIOServerWithKey(t, accountIOKey(), mutate)
}

func newAccountIOServerWithKey(t *testing.T, key []byte, mutate func(*Config)) (*httptest.Server, *Server, *store.Store, *secret.Store, *bytes.Buffer) {
	t.Helper()
	sealer, err := secret.New(key, "")
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	t.Cleanup(func() { _ = sealer.Close() })
	st := store.NewEmptyWithSealer(sealer)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := Config{AdminToken: testToken, Version: "test"}
	if mutate != nil {
		mutate(&cfg)
	}
	server := New(cfg, st, logger)
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts, server, st, sealer, &logs
}

func createAccountAPI(t *testing.T, ts *httptest.Server, label, username, password string) model.Account {
	t.Helper()
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts", map[string]any{
		"label":       label,
		"username":    username,
		"password":    password,
		"platform":    "linux",
		"concurrency": 2,
		"regions":     []string{"cn-zj"},
	})
	requireStatus(t, resp, http.StatusCreated)
	var account model.Account
	if err := json.Unmarshal(body, &account); err != nil {
		t.Fatalf("decode account: %v", err)
	}
	return account
}

func importRequest(kind string, content any, passphrase string, options store.AccountImportOptions) map[string]any {
	source := map[string]any{"kind": kind, "content": content}
	if passphrase != "" {
		source["passphrase"] = passphrase
	}
	return map[string]any{"source": source, "options": options}
}

func exportBody(format, passphrase string) map[string]any {
	body := map[string]any{"format": format}
	if passphrase != "" {
		body["passphrase"] = passphrase
	}
	if format != "encrypted" {
		body["confirmPlaintext"] = "EXPORT"
	}
	return body
}

func TestAccountIOCapabilities(t *testing.T) {
	ts, _, _, _, _ := newAccountIOServer(t, nil)
	resp, body := apiCall(t, ts, http.MethodGet, "/v1/accounts/io/capabilities", nil)
	requireStatus(t, resp, http.StatusOK)
	var capabilities store.AccountIOCapabilities
	if err := json.Unmarshal(body, &capabilities); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if !capabilities.EncryptedExport {
		t.Fatal("encryptedExport = false")
	}
	if capabilities.PlaintextExport {
		t.Fatal("plaintextExport = true without AJS_ALLOW_PLAINTEXT_EXPORT")
	}
	if capabilities.MaxImportRows != store.MaxImportRows || capabilities.MaxDeleteIDs != store.MaxDeleteIDs {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	if capabilities.MaxImportBodyBytes != store.MaxImportBodyBytes {
		t.Fatalf("maxImportBodyBytes = %d", capabilities.MaxImportBodyBytes)
	}

	gated, _, _, _, _ := newAccountIOServer(t, func(cfg *Config) { cfg.AllowPlaintextExport = true })
	resp, body = apiCall(t, gated, http.MethodGet, "/v1/accounts/io/capabilities", nil)
	requireStatus(t, resp, http.StatusOK)
	if err := json.Unmarshal(body, &capabilities); err != nil {
		t.Fatalf("decode gated capabilities: %v", err)
	}
	if !capabilities.PlaintextExport {
		t.Fatal("plaintextExport = false with AJS_ALLOW_PLAINTEXT_EXPORT")
	}
}

func TestAccountIOAuth(t *testing.T) {
	ts, _, _, _, _ := newAccountIOServer(t, nil)
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/accounts/io/capabilities"},
		{http.MethodPost, "/v1/accounts/export"},
		{http.MethodPost, "/v1/accounts/import"},
		{http.MethodPost, "/v1/accounts/bulk-delete"},
	}
	for _, tc := range cases {
		req := newRequest(t, tc.method, ts.URL+tc.path, map[string]any{})
		resp, body := doRequest(t, ts, req)
		requireStatus(t, resp, http.StatusUnauthorized)
		requireErrorCode(t, body, CodeUnauthorized)
	}
}

func TestAccountIOExportEncryptedRoundTrip(t *testing.T) {
	ts, _, st, _, _ := newAccountIOServer(t, nil)
	account := createAccountAPI(t, ts, "northwind", "vendor-northwind", "vendor-password")
	device := store.DeviceIdentity{
		UID: "6ada95c900de9bbb3a82", OSDID: "0123456789abcdef",
		CUID: "6ada95c900de9bbb3a82", CSEID: "linux", OSP: "linux", OSD: "x86_64",
		OSV: "Linux version 6.18.33.2", DIM: "lid=test", Version: "4.2.3.0", Site: "ajs",
	}
	if _, err := st.EnsureAccountDevice(account.ID, device); err != nil {
		t.Fatalf("EnsureAccountDevice: %v", err)
	}

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/export",
		exportBody("encrypted", accountIOPassphrase))
	requireStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get("Content-Type"); got != "application/vnd.ajs.accounts+json; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(got, `attachment; filename="ajs-accounts-`) || !strings.HasSuffix(got, `.ajsx"`) {
		t.Fatalf("content disposition = %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q", got)
	}
	if got := resp.Header.Get("X-AJS-Export-Count"); got != "1" {
		t.Fatalf("export count = %q", got)
	}
	if got := resp.Header.Get("X-AJS-Export-Missing-Secrets"); got != "0" {
		t.Fatalf("missing secrets = %q", got)
	}
	if got := resp.Header.Get("X-AJS-Export-Devices"); got != "1" {
		t.Fatalf("devices = %q", got)
	}
	if bytes.Contains(body, []byte("vendor-password")) {
		t.Fatal("encrypted export contains the plaintext password")
	}
	var envelope store.EncryptedExport
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	doc, err := store.OpenExport(envelope, accountIOPassphrase)
	if err != nil {
		t.Fatalf("OpenExport: %v", err)
	}
	if len(doc.Accounts) != 1 || doc.Accounts[0].Label != "northwind" {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.Accounts[0].Password == nil || *doc.Accounts[0].Password != "vendor-password" {
		t.Fatal("password did not round-trip")
	}
	if doc.Accounts[0].Device == nil || doc.Accounts[0].Device.UID != device.UID {
		t.Fatalf("device = %+v", doc.Accounts[0].Device)
	}

	// Import the envelope into a second daemon with a different master key.
	target, _, targetStore, _, _ := newAccountIOServerWithKey(t, []byte("abcdef0123456789abcdef0123456789"), nil)
	resp, body = apiCall(t, target, http.MethodPost, "/v1/accounts/import",
		importRequest("encrypted", envelope, accountIOPassphrase, store.AccountImportOptions{}))
	requireStatus(t, resp, http.StatusOK)
	var result store.AccountImportResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode import result: %v", err)
	}
	if !result.Applied || result.Counts.Created != 1 || result.Counts.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	accounts := targetStore.Accounts()
	if len(accounts) != 1 || accounts[0].Label != "northwind" || accounts[0].State != model.AccountPaused {
		t.Fatalf("accounts = %+v", accounts)
	}
	password, err := targetStore.AccountSecret(accounts[0].ID)
	if err != nil || string(password) != "vendor-password" {
		t.Fatalf("imported secret = %q, %v", password, err)
	}
	if importedDevice, ok := targetStore.AccountDevice(accounts[0].ID); !ok || importedDevice != device {
		t.Fatalf("imported device = %+v, %v", importedDevice, ok)
	}
}

func TestAccountIOExportPlaintextGate(t *testing.T) {
	ts, _, _, _, _ := newAccountIOServer(t, nil)
	createAccountAPI(t, ts, "northwind", "vendor-northwind", "vendor-password")

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/export", exportBody("json", ""))
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeConflict)

	gated, _, _, _, _ := newAccountIOServer(t, func(cfg *Config) { cfg.AllowPlaintextExport = true })
	createAccountAPI(t, gated, "northwind", "vendor-northwind", "vendor-password")

	resp, body = apiCall(t, gated, http.MethodPost, "/v1/accounts/export", map[string]any{"format": "json"})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	resp, body = apiCall(t, gated, http.MethodPost, "/v1/accounts/export", exportBody("json", ""))
	requireStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("JSON content type = %q", got)
	}
	var doc store.AccountExport
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode JSON export: %v", err)
	}
	if len(doc.Accounts) != 1 || doc.Accounts[0].Password == nil || *doc.Accounts[0].Password != "vendor-password" {
		t.Fatalf("doc = %+v", doc)
	}

	resp, body = apiCall(t, gated, http.MethodPost, "/v1/accounts/export", exportBody("csv", ""))
	requireStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("CSV content type = %q", got)
	}
	parsed, _, err := store.ParseExportCSV(body)
	if err != nil {
		t.Fatalf("ParseExportCSV: %v", err)
	}
	if len(parsed.Accounts) != 1 || parsed.Accounts[0].Label != "northwind" {
		t.Fatalf("parsed = %+v", parsed)
	}

	resp, body = apiCall(t, gated, http.MethodPost, "/v1/accounts/export", map[string]any{
		"format": "csv", "confirmPlaintext": "EXPORT", "includeDevice": true,
	})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
}

func TestAccountIOExportUnknownIDAndShortPassphrase(t *testing.T) {
	ts, _, _, _, _ := newAccountIOServer(t, nil)
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/export", map[string]any{
		"ids": []string{"acc-99"}, "format": "encrypted", "passphrase": accountIOPassphrase,
	})
	requireStatus(t, resp, http.StatusNotFound)
	requireErrorCode(t, body, CodeNotFound)

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/accounts/export", map[string]any{
		"format": "encrypted", "passphrase": "short",
	})
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
}

func TestAccountIOImportDryRun(t *testing.T) {
	source, _, _, _, _ := newAccountIOServer(t, func(cfg *Config) { cfg.AllowPlaintextExport = true })
	account := createAccountAPI(t, source, "northwind", "vendor-northwind", "vendor-password")
	resp, body := apiCall(t, source, http.MethodPost, "/v1/accounts/export", exportBody("json", ""))
	requireStatus(t, resp, http.StatusOK)
	var doc store.AccountExport
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode export: %v", err)
	}

	target, _, targetStore, targetSealer, _ := newAccountIOServer(t, nil)
	resp, body = apiCall(t, target, http.MethodPost, "/v1/accounts/import",
		importRequest("json", doc, "", store.AccountImportOptions{DryRun: true}))
	requireStatus(t, resp, http.StatusOK)
	var result store.AccountImportResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !result.DryRun || result.Applied || result.Counts.Created != 1 {
		t.Fatalf("dry-run result = %+v", result)
	}
	if len(targetStore.Accounts()) != 0 || len(targetSealer.IDs()) != 0 {
		t.Fatal("dry run mutated the store")
	}
	if len(targetStore.Audit(10)) != 0 {
		t.Fatal("dry run wrote audit")
	}

	resp, body = apiCall(t, target, http.MethodPost, "/v1/accounts/import",
		importRequest("json", doc, "", store.AccountImportOptions{
			IDPolicy:     "preserve",
			InitialState: "active",
		}))
	requireStatus(t, resp, http.StatusOK)
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode apply result: %v", err)
	}
	if !result.Applied || result.Counts.Created != 1 {
		t.Fatalf("apply result = %+v", result)
	}
	accounts := targetStore.Accounts()
	if len(accounts) != 1 || accounts[0].ID != account.ID || accounts[0].State != model.AccountActive {
		t.Fatalf("accounts = %+v", accounts)
	}
	if password, err := targetStore.AccountSecret(accounts[0].ID); err != nil || string(password) != "vendor-password" {
		t.Fatalf("secret = %q, %v", password, err)
	}
}

func TestAccountIOImportAtomicFailureAndPartial(t *testing.T) {
	password := "vendor-password"
	doc := store.AccountExport{
		Format:  store.AccountExportFormat,
		Version: 1,
		Accounts: []store.AccountExportRow{
			{Label: "good", Username: "vendor-good", Password: &password, Platform: model.PlatformLinux, Concurrency: 1, State: model.AccountPaused},
			{Label: "bad", Username: "vendor-bad", Password: &password, Platform: "solaris", Concurrency: 1, State: model.AccountPaused},
		},
	}
	ts, _, st, _, _ := newAccountIOServer(t, nil)
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/import",
		importRequest("json", doc, "", store.AccountImportOptions{}))
	requireStatus(t, resp, http.StatusOK)
	var result store.AccountImportResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Applied || result.Counts.Failed != 1 || result.Counts.Created != 1 {
		t.Fatalf("atomic result = %+v", result)
	}
	if len(st.Accounts()) != 0 {
		t.Fatal("atomic failure created an account")
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/accounts/import",
		importRequest("json", doc, "", store.AccountImportOptions{Mode: "partial"}))
	requireStatus(t, resp, http.StatusOK)
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode partial result: %v", err)
	}
	if !result.Applied || result.Counts.Created != 1 || result.Counts.Failed != 1 {
		t.Fatalf("partial result = %+v", result)
	}
	if len(st.Accounts()) != 1 || st.Accounts()[0].Label != "good" {
		t.Fatalf("accounts = %+v", st.Accounts())
	}
}

func TestAccountIOImportCSVAndWrongPassphrase(t *testing.T) {
	ts, _, st, _, _ := newAccountIOServer(t, nil)
	csvText := "label,username,password,platform,regions\ncsv-account,vendor-csv,csv-password,linux,cn-zj|hk\n"
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/import",
		importRequest("csv", csvText, "", store.AccountImportOptions{}))
	requireStatus(t, resp, http.StatusOK)
	var result store.AccountImportResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !result.Applied || result.Counts.Created != 1 {
		t.Fatalf("CSV result = %+v", result)
	}
	accounts := st.Accounts()
	if len(accounts) != 1 || len(accounts[0].Regions) != 2 {
		t.Fatalf("accounts = %+v", accounts)
	}

	// Build a valid envelope, then open it with the wrong passphrase.
	password := "vendor-password"
	doc := store.AccountExport{Format: store.AccountExportFormat, Version: 1, Accounts: []store.AccountExportRow{{
		Label: "enc", Username: "vendor-enc", Password: &password,
		Platform: model.PlatformLinux, Concurrency: 1, State: model.AccountPaused,
	}}}
	envelope, err := store.SealExport(doc, accountIOPassphrase, nil)
	if err != nil {
		t.Fatalf("SealExport: %v", err)
	}
	resp, body = apiCall(t, ts, http.MethodPost, "/v1/accounts/import",
		importRequest("encrypted", envelope, "wrong passphrase value", store.AccountImportOptions{}))
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)
	if !strings.Contains(string(body), "encrypted export could not be opened") {
		t.Fatalf("body = %s", body)
	}
	if len(st.Accounts()) != 1 {
		t.Fatal("wrong passphrase mutated the store")
	}
}

type fakeAccountIOPanel struct {
	mu         sync.Mutex
	reconciles int
}

func (p *fakeAccountIOPanel) Topology(ctx context.Context) (runtime.Topology, error) {
	return runtime.Topology{}, nil
}
func (p *fakeAccountIOPanel) Reconcile(ctx context.Context) (runtime.Topology, error) {
	p.mu.Lock()
	p.reconciles++
	p.mu.Unlock()
	return runtime.Topology{}, nil
}
func (p *fakeAccountIOPanel) PublishLease(ctx context.Context, lease model.Lease) error {
	return nil
}

func (p *fakeAccountIOPanel) WithdrawLease(context.Context, string) error { return nil }
func (p *fakeAccountIOPanel) RotateLease(ctx context.Context, leaseID string) (model.Lease, error) {
	return model.Lease{}, nil
}
func (p *fakeAccountIOPanel) ReleaseLease(ctx context.Context, leaseID string) error { return nil }
func (p *fakeAccountIOPanel) DrainNode(ctx context.Context, nodeID string) (model.ProxyNode, error) {
	return model.ProxyNode{}, nil
}
func (p *fakeAccountIOPanel) UndrainNode(ctx context.Context, nodeID string) (model.ProxyNode, error) {
	return model.ProxyNode{}, nil
}

type fakeAccountIOLogin struct {
	mu        sync.Mutex
	forgotten []string
}

func (f *fakeAccountIOLogin) Login(ctx context.Context, accountID string) (model.Account, error) {
	return model.Account{}, errors.New("not used")
}
func (f *fakeAccountIOLogin) Forget(accountID string) {
	f.mu.Lock()
	f.forgotten = append(f.forgotten, accountID)
	f.mu.Unlock()
}

func TestAccountIOBulkDelete(t *testing.T) {
	panel := &fakeAccountIOPanel{}
	login := &fakeAccountIOLogin{}
	var persists atomic.Int32
	ts, _, st, sealer, _ := newAccountIOServer(t, func(cfg *Config) {
		cfg.Panel = panel
		cfg.LoginService = login
		cfg.Persist = func() { persists.Add(1) }
	})
	first := createAccountAPI(t, ts, "one", "vendor-one", "password-one")
	second := createAccountAPI(t, ts, "two", "vendor-two", "password-two")

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/bulk-delete", map[string]any{
		"ids": []string{first.ID, second.ID, "acc-99"}, "dryRun": true,
	})
	requireStatus(t, resp, http.StatusOK)
	var result store.BulkDeleteResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode dry-run result: %v", err)
	}
	if result.Counts.WouldDelete != 2 || result.Counts.NotFound != 1 || result.Counts.Deleted != 0 {
		t.Fatalf("dry-run counts = %+v", result.Counts)
	}
	if len(st.Accounts()) != 2 || panel.reconciles != 0 || persists.Load() != 0 {
		t.Fatal("dry run mutated state")
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/accounts/bulk-delete", map[string]any{
		"ids": []string{first.ID, second.ID, "acc-99"},
	})
	requireStatus(t, resp, http.StatusOK)
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Counts.Deleted != 2 || result.Counts.NotFound != 1 || result.Counts.Total != 3 {
		t.Fatalf("counts = %+v", result.Counts)
	}
	if len(st.Accounts()) != 0 {
		t.Fatalf("accounts = %+v", st.Accounts())
	}
	if ids := sealer.IDs(); len(ids) != 0 {
		t.Fatalf("sealed ids after delete = %v", ids)
	}
	panel.mu.Lock()
	reconciles := panel.reconciles
	panel.mu.Unlock()
	if reconciles != 1 {
		t.Fatalf("reconciles = %d, want 1", reconciles)
	}
	if persists.Load() != 1 {
		t.Fatalf("persists = %d, want 1", persists.Load())
	}
	login.mu.Lock()
	forgotten := append([]string{}, login.forgotten...)
	login.mu.Unlock()
	if len(forgotten) != 2 {
		t.Fatalf("forgotten = %v", forgotten)
	}
	actions := make(map[string]int)
	for _, event := range st.Audit(20) {
		actions[event.Action]++
	}
	if actions["account.bulk_delete"] != 1 || actions["account.delete"] != 2 {
		t.Fatalf("audit actions = %v", actions)
	}
}

func TestAccountIOBulkDeleteHookFailureAndStop(t *testing.T) {
	panel := &fakeAccountIOPanel{}
	ts, _, st, _, _ := newAccountIOServer(t, func(cfg *Config) {
		cfg.Panel = panel
		cfg.AccountDeleteHook = func(ctx context.Context, accountID string) error {
			if accountID == "acc-02" {
				return errors.New("gate stuck")
			}
			return nil
		}
	})
	first := createAccountAPI(t, ts, "one", "vendor-one", "password-one")
	second := createAccountAPI(t, ts, "two", "vendor-two", "password-two")
	third := createAccountAPI(t, ts, "three", "vendor-three", "password-three")
	if first.ID != "acc-01" || second.ID != "acc-02" || third.ID != "acc-03" {
		t.Fatalf("ids = %s %s %s", first.ID, second.ID, third.ID)
	}

	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/bulk-delete", map[string]any{
		"ids": []string{first.ID, second.ID, third.ID}, "mode": "stop",
	})
	requireStatus(t, resp, http.StatusOK)
	var result store.BulkDeleteResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Counts.Deleted != 1 || result.Counts.Failed != 1 || result.Counts.Skipped != 1 {
		t.Fatalf("counts = %+v", result.Counts)
	}
	if result.Results[1].Status != "failed" || result.Results[2].Status != "skipped" {
		t.Fatalf("results = %+v", result.Results)
	}
	accounts := st.Accounts()
	if len(accounts) != 2 {
		t.Fatalf("accounts = %+v", accounts)
	}
	if _, ok := st.Account(second.ID); !ok {
		t.Fatal("failed account was deleted")
	}
	if _, ok := st.Account(third.ID); !ok {
		t.Fatal("skipped account was deleted")
	}
	panel.mu.Lock()
	reconciles := panel.reconciles
	panel.mu.Unlock()
	if reconciles != 1 {
		t.Fatalf("reconciles = %d, want 1", reconciles)
	}
}

func TestAccountIOBusy(t *testing.T) {
	ts, server, _, _, _ := newAccountIOServer(t, nil)
	createAccountAPI(t, ts, "one", "vendor-one", "password-one")
	server.accountIOMu.Lock()
	defer server.accountIOMu.Unlock()
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/export", exportBody("encrypted", accountIOPassphrase))
	requireStatus(t, resp, http.StatusConflict)
	requireErrorCode(t, body, CodeConflict)
}

func TestAccountIOLogRedaction(t *testing.T) {
	ts, _, st, _, logs := newAccountIOServer(t, nil)
	createAccountAPI(t, ts, "northwind", "vendor-northwind", "vendor-password")
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/export", exportBody("encrypted", accountIOPassphrase))
	requireStatus(t, resp, http.StatusOK)
	var envelope store.EncryptedExport
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	resp, _ = apiCall(t, ts, http.MethodPost, "/v1/accounts/import",
		importRequest("encrypted", envelope, accountIOPassphrase, store.AccountImportOptions{DryRun: true}))
	requireStatus(t, resp, http.StatusOK)
	// A failed open must log a reason code, never the passphrase.
	resp, _ = apiCall(t, ts, http.MethodPost, "/v1/accounts/import",
		importRequest("encrypted", envelope, "wrong passphrase value", store.AccountImportOptions{}))
	requireStatus(t, resp, http.StatusBadRequest)

	logged := logs.String()
	for _, secretValue := range []string{"vendor-password", accountIOPassphrase, "wrong passphrase value"} {
		if strings.Contains(logged, secretValue) {
			t.Fatalf("log leaked %q: %s", secretValue, logged)
		}
	}
	for _, event := range st.Audit(20) {
		if strings.Contains(event.Detail, "vendor-password") || strings.Contains(event.Detail, accountIOPassphrase) {
			t.Fatalf("audit leaked a credential: %+v", event)
		}
	}
}

func TestAccountIOCORSExposesExportHeaders(t *testing.T) {
	ts, _, _, _, _ := newAccountIOServer(t, nil)
	req := newRequest(t, http.MethodGet, ts.URL+"/v1/accounts", nil)
	req.Header.Set("Origin", "http://127.0.0.1:18081")
	resp, _ := doRequest(t, ts, req)
	requireStatus(t, resp, http.StatusUnauthorized)
	exposed := resp.Header.Get("Access-Control-Expose-Headers")
	for _, header := range []string{"Content-Disposition", "X-AJS-Export-Count", "X-AJS-Export-Missing-Secrets", "X-AJS-Export-Devices"} {
		if !strings.Contains(exposed, header) {
			t.Fatalf("expose headers = %q, missing %s", exposed, header)
		}
	}
}

func TestAccountIOImportBodyLimitIsFourMiB(t *testing.T) {
	ts, _, _, _, _ := newAccountIOServer(t, nil)
	oversized := strings.Repeat("x", (4<<20)+1)
	resp, body := apiCall(t, ts, http.MethodPost, "/v1/accounts/import", map[string]any{
		"source":  map[string]any{"kind": "csv", "content": oversized},
		"options": map[string]any{},
	})
	requireStatus(t, resp, http.StatusRequestEntityTooLarge)
	requireErrorCode(t, body, CodeInvalidArgument)
}

func TestAccountIOCSVExportFilename(t *testing.T) {
	ts, _, _, _, _ := newAccountIOServer(t, func(cfg *Config) { cfg.AllowPlaintextExport = true })
	createAccountAPI(t, ts, "northwind", "vendor-northwind", "vendor-password")
	resp, _ := apiCall(t, ts, http.MethodPost, "/v1/accounts/export", exportBody("csv", ""))
	requireStatus(t, resp, http.StatusOK)
	disposition := resp.Header.Get("Content-Disposition")
	if !strings.Contains(disposition, ".csv") {
		t.Fatalf("disposition = %q", disposition)
	}
	if !strings.Contains(resp.Header.Get("X-AJS-Export-Count"), "1") {
		t.Fatalf("count = %q", resp.Header.Get("X-AJS-Export-Count"))
	}
}
