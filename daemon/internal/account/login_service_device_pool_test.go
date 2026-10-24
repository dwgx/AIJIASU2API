package account

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/secret"
	"ajsaccountd/internal/store"
)

// newPoolStore builds the daemon's pool-enabled shape: a password sealer and a
// distinct device sealer. The keys are fixed local test values, not
// credentials.
func newPoolStore(t *testing.T) (*store.Store, *secret.Store, *secret.Store) {
	t.Helper()
	dir := t.TempDir()
	passwords, err := secret.New(bytes.Repeat([]byte{0x41}, secret.KeySize), filepath.Join(dir, "secrets.json"))
	if err != nil {
		t.Fatalf("password sealer: %v", err)
	}
	devices, err := secret.New(bytes.Repeat([]byte{0x42}, secret.KeySize), filepath.Join(dir, "device-secrets.json"))
	if err != nil {
		t.Fatalf("device sealer: %v", err)
	}
	t.Cleanup(func() {
		_ = passwords.Close()
		_ = devices.Close()
	})
	return store.NewEmptyWithSealers(passwords, devices), passwords, devices
}

// TestDeviceForBindsPoolMaterial is the login-side wiring contract: with the
// pool enabled, the first login binds a sealed pool record and every later
// login reuses that material instead of generating a new identity.
func TestDeviceForBindsPoolMaterial(t *testing.T) {
	st, passwords, devices := newPoolStore(t)
	account := newLoginAccount(t, st)
	service := newTestLoginService(t, st, "https://login.invalid", nil)

	first, err := service.deviceFor(account.ID)
	if err != nil {
		t.Fatalf("deviceFor first: %v", err)
	}
	second, err := service.deviceFor(account.ID)
	if err != nil {
		t.Fatalf("deviceFor second: %v", err)
	}
	if first != second {
		t.Fatalf("device identity changed between calls: %+v -> %+v", first, second)
	}
	if !st.DevicePoolEnabled() {
		t.Fatal("store reports the device pool disabled")
	}

	records := st.DevicePoolRecords()
	if len(records) != 1 {
		t.Fatalf("pool records = %d, want 1", len(records))
	}
	rec := records[0]
	if rec.State != store.DeviceAssigned || rec.AssignedAccountID != account.ID {
		t.Fatalf("bound record = %+v, want assigned to %s", rec, account.ID)
	}
	if rec.PresentedAt == nil {
		t.Fatal("bound record has no presentedAt; the identity was not marked as presented")
	}
	if rec.Profile != Linux423Binary.Name {
		t.Fatalf("bound profile = %q, want %q", rec.Profile, Linux423Binary.Name)
	}
	if rec.Algorithm != store.DeviceAlgorithmRandom {
		t.Fatalf("bound algorithm = %q, want %q", rec.Algorithm, store.DeviceAlgorithmRandom)
	}

	material, err := st.AccountDeviceMaterial(account.ID)
	if err != nil {
		t.Fatalf("AccountDeviceMaterial: %v", err)
	}
	if material.Identity != first {
		t.Fatalf("sealed identity = %+v, want the value login used %+v", material.Identity, first)
	}
	if got, ok := st.AccountDevice(account.ID); !ok || got != first {
		t.Fatalf("account mirror = %+v (present %v), want %+v", got, ok, first)
	}

	// The device material lives in the device sealer, not the password one.
	if ids := devices.IDs(); len(ids) != 1 || ids[0] != rec.ID {
		t.Fatalf("device sealer ids = %v, want [%s]", ids, rec.ID)
	}
	if ids := passwords.IDs(); len(ids) != 1 || ids[0] != account.ID {
		t.Fatalf("password sealer ids = %v, want [%s]", ids, account.ID)
	}
}

// TestLoginPresentsPoolMaterialAndLogsNoIdentity runs the full login path with
// the pool enabled: the vendor request carries the sealed material's _uid, and
// no log line carries a raw identity field or the machine code.
func TestLoginPresentsPoolMaterialAndLogsNoIdentity(t *testing.T) {
	st, _, _ := newPoolStore(t)
	account := newLoginAccount(t, st)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	server, requests := newFake(t, func(capturedRequest) (int, string) {
		return http.StatusOK, loginReply
	})
	service := newTestLoginService(t, st, server.URL, logger)
	// MarkLogin needs a later tick than CreateAccount on the Windows clock.
	time.Sleep(20 * time.Millisecond)

	if _, err := service.Login(context.Background(), account.ID); err != nil {
		t.Fatalf("Login: %v", err)
	}

	var login capturedRequest
	for {
		request := <-requests
		if request.form.Get("_client_login") == "" {
			login = request
			break
		}
	}
	material, err := st.AccountDeviceMaterial(account.ID)
	if err != nil {
		t.Fatalf("AccountDeviceMaterial: %v", err)
	}
	if got := login.form.Get("_uid"); got != material.Identity.UID {
		t.Fatalf("wire _uid = %q, want the sealed identity %q", got, material.Identity.UID)
	}

	for _, raw := range []string{
		material.Identity.UID,
		material.Identity.OSDID,
		material.Identity.CUID,
		material.MachineCode,
	} {
		if raw == "" {
			continue
		}
		if strings.Contains(logs.String(), raw) {
			t.Fatalf("log output carries raw device value %q: %s", raw, logs.String())
		}
	}
}
