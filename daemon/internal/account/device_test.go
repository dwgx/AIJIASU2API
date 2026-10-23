package account

import (
	"strings"
	"testing"

	"ajsaccountd/internal/store"
)

// TestDeviceForIdentity pins the runtime counterpart of Identify: the device
// the controller opens a session with is the device the login presented.
func TestDeviceForIdentity(t *testing.T) {
	device, err := NewDeviceForProfile(Linux423Binary)
	if err != nil {
		t.Fatalf("NewDeviceForProfile: %v", err)
	}
	rebuilt, err := DeviceForIdentity(Identify(device))
	if err != nil {
		t.Fatalf("DeviceForIdentity: %v", err)
	}
	if rebuilt != device {
		t.Fatalf("rebuilt device = %+v, want %+v", rebuilt, device)
	}
}

func TestDeviceForIdentityRejectsIncomplete(t *testing.T) {
	cases := []struct {
		name     string
		identity store.DeviceIdentity
	}{
		{name: "empty", identity: store.DeviceIdentity{}},
		{name: "no uid", identity: store.DeviceIdentity{OSDID: "0123456789abcdef", CUID: "x", CSEID: "linux"}},
		{name: "no osdid", identity: store.DeviceIdentity{UID: "x", CUID: "x", CSEID: "linux"}},
		{name: "no cuid", identity: store.DeviceIdentity{UID: "x", OSDID: "0123456789abcdef", CSEID: "linux"}},
		{name: "no cseid", identity: store.DeviceIdentity{UID: "x", OSDID: "0123456789abcdef", CUID: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DeviceForIdentity(tc.identity)
			if err == nil || !strings.Contains(err.Error(), "incomplete") {
				t.Fatalf("DeviceForIdentity error = %v, want an incomplete-identity error", err)
			}
		})
	}
}
