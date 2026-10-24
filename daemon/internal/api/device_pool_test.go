package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ajsaccountd/internal/model"
	"ajsaccountd/internal/secret"
	"ajsaccountd/internal/store"
	"ajsaccountd/internal/webhook"
)

const devicePoolTestPassphrase = "device-pool-api-passphrase"

type devicePoolAPI struct {
	ts     *httptest.Server
	st     *store.Store
	sealer *secret.Store
	logs   *bytes.Buffer
}

// newDevicePoolAPI builds a server whose store has a real sealed-material
// file, so generate/export/import exercise the same code path as the daemon.
func newDevicePoolAPI(t *testing.T) *devicePoolAPI {
	t.Helper()
	return newDevicePoolAPIWithEvents(t, nil)
}

func newDevicePoolAPIWithEvents(t *testing.T, events EventSink) *devicePoolAPI {
	t.Helper()
	dir := t.TempDir()
	passwords, err := secret.New(bytes.Repeat([]byte{0x31}, secret.KeySize), filepath.Join(dir, "secrets.json"))
	if err != nil {
		t.Fatalf("password sealer: %v", err)
	}
	devices, err := secret.New(bytes.Repeat([]byte{0x32}, secret.KeySize), filepath.Join(dir, "device-secrets.json"))
	if err != nil {
		t.Fatalf("device sealer: %v", err)
	}
	st := store.NewEmptyWithSealers(passwords, devices)
	logs := &bytes.Buffer{}
	server := New(Config{AdminToken: testToken, Events: events}, st, slog.New(slog.NewTextHandler(logs, nil)))
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return &devicePoolAPI{ts: ts, st: st, sealer: devices, logs: logs}
}

func (a *devicePoolAPI) createAccount(t *testing.T, label, username string) model.Account {
	t.Helper()
	account, err := a.st.CreateAccount(model.CreateAccountInput{
		Label: label, Username: username, Password: "device-pool-api-password",
		Platform: model.PlatformLinux, Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateAccount(%q): %v", label, err)
	}
	return account
}

func (a *devicePoolAPI) generate(t *testing.T, count int) []deviceRecordView {
	t.Helper()
	resp, body := apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/generate", deviceGenerateRequest{
		Count: count, Profile: "linux-4.2.3.0-binary", LabelPrefix: "linux",
	})
	requireStatus(t, resp, http.StatusCreated)
	return decodeBody[deviceGenerateResponse](t, body).Devices
}

func (a *devicePoolAPI) list(t *testing.T) devicePoolView {
	t.Helper()
	return a.listQuery(t, "/v1/device-pool")
}

func (a *devicePoolAPI) listQuery(t *testing.T, path string) devicePoolView {
	t.Helper()
	resp, body := apiCall(t, a.ts, http.MethodGet, path, nil)
	requireStatus(t, resp, http.StatusOK)
	return decodeBody[devicePoolView](t, body)
}

func requireDeviceError(t *testing.T, body []byte, status int, code, reason string) {
	t.Helper()
	var payload errorResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	if payload.Error.Code != code || payload.Error.Reason != reason {
		t.Fatalf("error = %s/%s, want %s/%s (body %s)",
			payload.Error.Code, payload.Error.Reason, code, reason, body)
	}
}

func openDeviceMaterial(t *testing.T, sealer *secret.Store, id string) store.DeviceMaterial {
	t.Helper()
	raw, err := sealer.OpenID(id)
	if err != nil {
		t.Fatalf("OpenID(%q): %v", id, err)
	}
	var material store.DeviceMaterial
	if err := json.Unmarshal(raw, &material); err != nil {
		t.Fatalf("decode material %q: %v", id, err)
	}
	return material
}

// TestDevicePoolDisabled pins the design's disabled-pool contract: the list
// is a 200 with enabled:false and a reason, mutations are 409.
func TestDevicePoolDisabled(t *testing.T) {
	st := store.New()
	server := New(Config{AdminToken: testToken}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server)
	defer ts.Close()

	resp, body := apiCall(t, ts, http.MethodGet, "/v1/device-pool", nil)
	requireStatus(t, resp, http.StatusOK)
	view := decodeBody[devicePoolView](t, body)
	if view.Enabled || view.Reason != store.DeviceReasonPoolDisabled {
		t.Fatalf("disabled list = enabled:%t reason:%q, want false/%q",
			view.Enabled, view.Reason, store.DeviceReasonPoolDisabled)
	}
	if view.Total != 0 || len(view.Devices) != 0 {
		t.Fatalf("disabled list = total:%d devices:%d, want 0/0", view.Total, len(view.Devices))
	}
	if view.Devices == nil {
		t.Fatal("disabled list devices is null, want []")
	}

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/device-pool/generate", deviceGenerateRequest{Count: 1})
	requireStatus(t, resp, http.StatusConflict)
	requireDeviceError(t, body, http.StatusConflict, CodeConflict, store.DeviceReasonPoolDisabled)

	resp, body = apiCall(t, ts, http.MethodPost, "/v1/device-pool/export", deviceExportRequest{
		Passphrase: devicePoolTestPassphrase, Scope: "all",
	})
	requireStatus(t, resp, http.StatusConflict)
	requireDeviceError(t, body, http.StatusConflict, CodeConflict, store.DeviceReasonPoolDisabled)
}

// TestDevicePoolGenerateViewRedacted proves the generate response is the
// hash-only view: hashes carry sha256 prefixes, claims omit cseid, and no raw
// identity value or raw-identity field appears in the body or the log.
func TestDevicePoolGenerateViewRedacted(t *testing.T) {
	a := newDevicePoolAPI(t)
	devices := a.generate(t, 2)
	if len(devices) != 2 {
		t.Fatalf("generated %d devices, want 2", len(devices))
	}
	for _, device := range devices {
		if device.State != store.DeviceAvailable {
			t.Fatalf("state = %q, want available", device.State)
		}
		if device.Platform != model.PlatformLinux || device.Profile != "linux-4.2.3.0-binary" {
			t.Fatalf("platform/profile = %s/%s", device.Platform, device.Profile)
		}
		for _, key := range []string{"uid", "osdid", "cuid", "cseid", "machine"} {
			value := device.Hashes[key]
			if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+16 {
				t.Fatalf("hash %q = %q, want sha256:<16 hex>", key, value)
			}
		}
		if _, ok := device.Claims["cseid"]; ok {
			t.Fatal("claims must not contain cseid")
		}
		if !device.MaterialPresent || device.LoginCount != 0 || device.PresentedAt != nil {
			t.Fatalf("fresh record = %+v, want material present, no presentation", device)
		}
	}

	// The serialized page must not contain the raw identity of any record.
	_, pageBody := apiCall(t, a.ts, http.MethodGet, "/v1/device-pool", nil)
	page := string(pageBody)

	filtered := a.listQuery(t, "/v1/device-pool?state=available&platform=linux&q=linux&limit=1&offset=0")
	if filtered.Total != 2 || len(filtered.Devices) != 1 {
		t.Fatalf("filtered page = total:%d devices:%d, want 2/1", filtered.Total, len(filtered.Devices))
	}
	if filtered.Counts[string(store.DeviceAvailable)] != 2 {
		t.Fatalf("available count = %d, want 2", filtered.Counts[string(store.DeviceAvailable)])
	}
	resp, detailBody := apiCall(t, a.ts, http.MethodGet, "/v1/device-pool/"+devices[0].ID, nil)
	requireStatus(t, resp, http.StatusOK)
	if detail := decodeBody[deviceRecordView](t, detailBody); detail.ID != devices[0].ID {
		t.Fatalf("detail id = %q, want %q", detail.ID, devices[0].ID)
	}
	resp, body := apiCall(t, a.ts, http.MethodGet, "/v1/device-pool?limit=nope", nil)
	requireStatus(t, resp, http.StatusBadRequest)
	requireErrorCode(t, body, CodeInvalidArgument)

	responses := []string{page, string(detailBody)}
	for _, forbidden := range []string{`"machineCode"`, `"identity"`} {
		for _, response := range responses {
			if strings.Contains(response, forbidden) {
				t.Fatalf("pool response leaks %s: %s", forbidden, response)
			}
		}
	}
	for _, device := range devices {
		material := openDeviceMaterial(t, a.sealer, device.ID)
		for _, raw := range []string{
			material.MachineCode, material.Identity.UID, material.Identity.OSDID,
			material.Identity.CUID, material.Identity.DIM,
		} {
			if raw == "" {
				t.Fatal("sealed material has an empty identity field")
			}
			for _, response := range responses {
				if strings.Contains(response, raw) {
					t.Fatalf("pool response contains raw identity %q", raw)
				}
			}
			if strings.Contains(a.logs.String(), raw) {
				t.Fatalf("request log contains raw identity %q", raw)
			}
		}
	}
}

// TestDevicePoolLifecycleGuards walks every mutation guard from the design
// table: bind conflicts, live-session release, assigned quarantine/purge,
// confirm tokens and the final purge.
func TestDevicePoolLifecycleGuards(t *testing.T) {
	a := newDevicePoolAPI(t)
	first := a.createAccount(t, "First", "first-user")
	second := a.createAccount(t, "Second", "second-user")
	devices := a.generate(t, 2)
	one, two := devices[0].ID, devices[1].ID

	resp, body := apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+one+"/assign",
		deviceAssignRequest{AccountID: first.ID})
	requireStatus(t, resp, http.StatusOK)
	assigned := decodeBody[deviceRecordView](t, body)
	if assigned.State != store.DeviceAssigned || assigned.AccountID != first.ID || assigned.AccountLabel != "First" {
		t.Fatalf("assigned view = %+v", assigned)
	}

	resp, body = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+two+"/assign",
		deviceAssignRequest{AccountID: first.ID})
	requireStatus(t, resp, http.StatusConflict)
	requireDeviceError(t, body, http.StatusConflict, CodeConflict, store.DeviceReasonAlreadyBound)

	resp, body = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+one+"/assign",
		deviceAssignRequest{AccountID: second.ID})
	requireStatus(t, resp, http.StatusConflict)
	requireDeviceError(t, body, http.StatusConflict, CodeConflict, store.DeviceReasonStateInvalid)

	resp, body = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+one+"/quarantine",
		deviceReasonConfirmRequest{Reason: "bad", Confirm: "QUARANTINE"})
	requireStatus(t, resp, http.StatusConflict)
	requireDeviceError(t, body, http.StatusConflict, CodeConflict, store.DeviceReasonStateInvalid)

	resp, body = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+one+"/release",
		deviceConfirmRequest{})
	requireStatus(t, resp, http.StatusBadRequest)
	requireDeviceError(t, body, http.StatusBadRequest, CodeInvalidArgument, store.DeviceReasonConfirmRequired)

	node, err := a.st.PutNode(model.ProxyNode{
		ID: "node-live", AccountID: first.ID, Label: "live-" + first.ID,
		Region: "cn", State: model.NodeLive, Capacity: 1,
	})
	if err != nil {
		t.Fatalf("PutNode: %v", err)
	}
	resp, body = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+one+"/release",
		deviceConfirmRequest{Confirm: "RELEASE"})
	requireStatus(t, resp, http.StatusConflict)
	requireDeviceError(t, body, http.StatusConflict, CodeConflict, store.DeviceReasonActiveSession)

	if err := a.st.DeleteNode(node.ID); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	resp, body = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+one+"/release",
		deviceConfirmRequest{Confirm: "RELEASE"})
	requireStatus(t, resp, http.StatusOK)
	released := decodeBody[deviceRecordView](t, body)
	if released.State != store.DeviceAvailable || released.AccountID != "" {
		t.Fatalf("released view = %+v, want available and unbound", released)
	}

	resp, body = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+one+"/retire",
		deviceReasonConfirmRequest{Reason: "cleanup"})
	requireStatus(t, resp, http.StatusBadRequest)
	requireDeviceError(t, body, http.StatusBadRequest, CodeInvalidArgument, store.DeviceReasonConfirmRequired)

	resp, body = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+one+"/retire",
		deviceReasonConfirmRequest{Reason: "cleanup", Confirm: "RETIRE"})
	requireStatus(t, resp, http.StatusOK)
	retired := decodeBody[deviceRecordView](t, body)
	if retired.State != store.DeviceRetired || retired.RetiredReason != "cleanup" || retired.RetiredAt == nil {
		t.Fatalf("retired view = %+v", retired)
	}

	resp, body = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+one+"/quarantine",
		deviceReasonConfirmRequest{Reason: "leak", Confirm: "QUARANTINE"})
	requireStatus(t, resp, http.StatusOK)
	if quarantined := decodeBody[deviceRecordView](t, body); quarantined.State != store.DeviceQuarantined {
		t.Fatalf("quarantined view = %+v", quarantined)
	}

	resp, body = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+one+"/assign",
		deviceAssignRequest{AccountID: first.ID})
	requireStatus(t, resp, http.StatusConflict)
	requireDeviceError(t, body, http.StatusConflict, CodeConflict, store.DeviceReasonStateInvalid)

	resp, body = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+one+"/purge",
		deviceConfirmRequest{Confirm: "PURGE"})
	requireStatus(t, resp, http.StatusNoContent)
	if len(body) != 0 {
		t.Fatalf("purge body = %q, want empty", body)
	}
	resp, body = apiCall(t, a.ts, http.MethodGet, "/v1/device-pool/"+one, nil)
	requireStatus(t, resp, http.StatusNotFound)
	requireDeviceError(t, body, http.StatusNotFound, CodeNotFound, store.DeviceReasonNotFound)

	// An assigned record cannot be purged, and a PATCH only edits operator text.
	resp, body = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+two+"/assign",
		deviceAssignRequest{AccountID: second.ID})
	requireStatus(t, resp, http.StatusOK)
	resp, body = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+two+"/purge",
		deviceConfirmRequest{Confirm: "PURGE"})
	requireStatus(t, resp, http.StatusConflict)
	requireDeviceError(t, body, http.StatusConflict, CodeConflict, store.DeviceReasonStateInvalid)

	label, notes := "renamed", "operator note"
	resp, body = apiCall(t, a.ts, http.MethodPatch, "/v1/device-pool/"+two,
		devicePatchRequest{Label: &label, Notes: &notes})
	requireStatus(t, resp, http.StatusOK)
	patched := decodeBody[deviceRecordView](t, body)
	if patched.Label != label || patched.Notes != notes {
		t.Fatalf("patched view = %+v", patched)
	}

	resp, _ = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+two+"/release",
		deviceConfirmRequest{Confirm: "RELEASE"})
	requireStatus(t, resp, http.StatusOK)
}

// TestDevicePoolExportImportRoundTrip exports from one daemon, dry-runs the
// import on a fresh one (no mutation), commits it, and proves the second run
// is idempotent. Neither response may carry raw identity.
func TestDevicePoolExportImportRoundTrip(t *testing.T) {
	source := newDevicePoolAPI(t)
	account := source.createAccount(t, "Exported", "exported-user")
	devices := source.generate(t, 2)
	resp, body := apiCall(t, source.ts, http.MethodPost, "/v1/device-pool/"+devices[0].ID+"/assign",
		deviceAssignRequest{AccountID: account.ID})
	requireStatus(t, resp, http.StatusOK)

	resp, body = apiCall(t, source.ts, http.MethodPost, "/v1/device-pool/export", deviceExportRequest{
		Passphrase: devicePoolTestPassphrase, Scope: "all",
	})
	requireStatus(t, resp, http.StatusOK)
	envelope := decodeBody[store.DeviceExportEnvelope](t, body)
	if envelope.Format != store.DeviceFormat || envelope.Version != store.DeviceFormatVersion {
		t.Fatalf("envelope = %s v%d", envelope.Format, envelope.Version)
	}
	if envelope.DeviceCount != 2 || envelope.Ciphertext == "" || envelope.PayloadSHA256 == "" {
		t.Fatalf("envelope = %+v", envelope)
	}
	exportBody := string(body)
	for _, forbidden := range []string{`"machineCode"`, `"identity"`, `"uid"`, `"cseid"`} {
		if strings.Contains(exportBody, forbidden) {
			t.Fatalf("export response leaks %s", forbidden)
		}
	}

	target := newDevicePoolAPI(t)
	importRequest := deviceImportRequest{
		Passphrase: devicePoolTestPassphrase, Envelope: body, DryRun: true,
	}
	resp, dryBody := apiCall(t, target.ts, http.MethodPost, "/v1/device-pool/import", importRequest)
	requireStatus(t, resp, http.StatusOK)
	dry := decodeBody[store.DeviceImportResult](t, dryBody)
	if !dry.DryRun || dry.Added != 2 || len(dry.Devices) != 2 {
		t.Fatalf("dry run = %+v, want 2 added", dry)
	}
	if list := target.list(t); list.Total != 0 {
		t.Fatalf("dry run mutated the pool: total %d", list.Total)
	}

	importRequest.DryRun = false
	resp, importBody := apiCall(t, target.ts, http.MethodPost, "/v1/device-pool/import", importRequest)
	requireStatus(t, resp, http.StatusOK)
	imported := decodeBody[store.DeviceImportResult](t, importBody)
	if imported.DryRun || imported.Added != 2 {
		t.Fatalf("import = %+v, want 2 added", imported)
	}
	if list := target.list(t); list.Total != 2 {
		t.Fatalf("imported pool total = %d, want 2", list.Total)
	}
	if strings.Contains(string(importBody), `"machineCode"`) || strings.Contains(string(importBody), `"identity"`) {
		t.Fatal("import result leaks raw identity fields")
	}

	resp, againBody := apiCall(t, target.ts, http.MethodPost, "/v1/device-pool/import", importRequest)
	requireStatus(t, resp, http.StatusOK)
	again := decodeBody[store.DeviceImportResult](t, againBody)
	if again.Added != 0 || again.Skipped != 2 {
		t.Fatalf("second import = %+v, want idempotent skip", again)
	}

	for _, id := range []string{devices[0].ID, devices[1].ID} {
		material := openDeviceMaterial(t, source.sealer, id)
		for _, raw := range []string{material.MachineCode, material.Identity.UID} {
			if strings.Contains(string(importBody), raw) {
				t.Fatalf("import response contains raw identity %q", raw)
			}
			if strings.Contains(target.logs.String(), raw) || strings.Contains(source.logs.String(), raw) {
				t.Fatalf("request log contains raw identity %q", raw)
			}
		}
	}
}

// TestDeviceRetireEmitsEvent pins the device.retired payload: the retired
// device id and reason always travel, the account id only when the record was
// bound at retire time (RetireDevice unbinds it).
func TestDeviceRetireEmitsEvent(t *testing.T) {
	sink := &recordingSink{}
	a := newDevicePoolAPIWithEvents(t, sink)
	account := a.createAccount(t, "Retire", "retire-user")
	devices := a.generate(t, 2)

	resp, _ := apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+devices[0].ID+"/assign",
		deviceAssignRequest{AccountID: account.ID})
	requireStatus(t, resp, http.StatusOK)

	resp, body := apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+devices[0].ID+"/retire",
		deviceReasonConfirmRequest{Reason: "hardware replaced", Confirm: "RETIRE"})
	requireStatus(t, resp, http.StatusOK)
	if retired := decodeBody[deviceRecordView](t, body); retired.State != store.DeviceRetired {
		t.Fatalf("retired view = %+v", retired)
	}

	if got := sink.count(webhook.EventDeviceRetired); got != 1 {
		t.Fatalf("device.retired emitted %d times, want 1 (events %v)", got, sink.types())
	}
	sink.mu.Lock()
	events := append([]recordedEvent(nil), sink.events...)
	sink.mu.Unlock()
	first := events[len(events)-1].data
	if first["deviceId"] != devices[0].ID || first["reason"] != "hardware replaced" || first["accountId"] != account.ID {
		t.Fatalf("device.retired payload = %#v", first)
	}

	// An unbound retire carries no accountId key at all.
	resp, _ = apiCall(t, a.ts, http.MethodPost, "/v1/device-pool/"+devices[1].ID+"/retire",
		deviceReasonConfirmRequest{Reason: "spare", Confirm: "RETIRE"})
	requireStatus(t, resp, http.StatusOK)
	sink.mu.Lock()
	events = append([]recordedEvent(nil), sink.events...)
	sink.mu.Unlock()
	second := events[len(events)-1].data
	if second["deviceId"] != devices[1].ID || second["reason"] != "spare" {
		t.Fatalf("unbound device.retired payload = %#v", second)
	}
	if _, present := second["accountId"]; present {
		t.Fatalf("unbound retire payload carries accountId: %#v", second)
	}
}
