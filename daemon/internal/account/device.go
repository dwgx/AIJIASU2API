package account

import (
	"errors"
	"strings"

	"ajsaccountd/internal/store"
	"ajsproxy/apifacade"
)

// Client versions of the supported profiles. LinuxClientVersion is the
// default: the daemon emulates the Linux client unless told otherwise.
const (
	// LinuxClientVersion is the _v value of the Linux 4.2.3.0 client.
	LinuxClientVersion = apifacade.Linux423Version
	// Linux430ClientVersion is the _v value of the Linux 4.3.0.1 client.
	// The device descriptor did not change between the two Linux builds
	// (research/84-ajs-496/LINUX.md §7), so it uses the captured Linux
	// descriptor with this version claim.
	Linux430ClientVersion = "4.3.0.1"
	// OpenAJSClientVersion is the _v value of the OpenAJS Android client.
	OpenAJSClientVersion = "4.11.10.0"
)

// NewDevice returns a device descriptor with generated IDs for the given
// client version. An empty version selects LinuxClientVersion.
//
// Both Linux client versions build the captured Linux descriptor: linux /
// x86_64, the captured _os_ver and _os_dim, a 20-hex _uid that doubles as cuid,
// and cseid=linux. Any other version builds the OpenAJS/Android descriptor from
// apifacade.DefaultDevice with a 22-character _uid and 16-hex _os_did.
func NewDevice(version string) (apifacade.Device, error) {
	if version == "" {
		version = LinuxClientVersion
	}
	if version == LinuxClientVersion || version == Linux430ClientVersion {
		device := apifacade.Linux423Device()
		device.Version = version
		if err := device.EnsureLinuxIDs(); err != nil {
			return apifacade.Device{}, err
		}
		return device, nil
	}
	device := apifacade.DefaultDevice()
	device.Version = version
	if err := device.EnsureIDs(); err != nil {
		return apifacade.Device{}, err
	}
	return device, nil
}

// NewDeviceForProfile returns the device descriptor the profile uses. It is the
// builder LoginService calls for a first login; later logins reuse the stored
// identity instead.
func NewDeviceForProfile(profile VersionProfile) (apifacade.Device, error) {
	if profile.ClientVersion == "" {
		return apifacade.Device{}, errors.New("account: profile has no client version")
	}
	return NewDevice(profile.ClientVersion)
}

// Identify summarizes device as the stable, non-secret record the store keeps.
//
// An empty CUID falls back to DeviceID and an empty CSEID to CUID, so an
// OpenAJS device records the same value twice and a Linux device records its
// 20-hex cuid and cseid=linux.
func Identify(device apifacade.Device) store.DeviceIdentity {
	cuid := device.CUID
	if cuid == "" {
		cuid = device.DeviceID
	}
	cseid := device.CSEID
	if cseid == "" {
		cseid = cuid
	}
	return store.DeviceIdentity{
		UID:     device.UID,
		OSDID:   device.DeviceID,
		CUID:    cuid,
		CSEID:   cseid,
		OSP:     device.Platform,
		OSD:     device.OSDevice,
		OSV:     device.OSVersion,
		DIM:     device.OSDim,
		Version: device.Version,
		Site:    device.Site,
		HW:      device.OSHW,
		OSLang:  device.OSLang,
		TZMin:   device.TZMinutes,
		Lang:    device.Lang,
		InstMkt: device.InstMkt,
		Vrf:     device.Vrf,
	}
}

// DeviceForIdentity rebuilds the control and FV descriptor from the identity
// the login service stored for an account. It is the runtime counterpart of
// Identify: the node controller opens a session with exactly the device the
// login presented.
//
// A record missing one of UID/OSDID/CUID/CSEID is rejected; EnsureAccountDevice
// stores only complete records, so a rejection means the snapshot was edited.
func DeviceForIdentity(identity store.DeviceIdentity) (apifacade.Device, error) {
	if strings.TrimSpace(identity.UID) == "" ||
		strings.TrimSpace(identity.OSDID) == "" ||
		strings.TrimSpace(identity.CUID) == "" ||
		strings.TrimSpace(identity.CSEID) == "" {
		return apifacade.Device{}, errors.New("account: stored device identity is incomplete (need uid/osdid/cuid/cseid)")
	}
	return deviceFromIdentity(identity, ""), nil
}

// deviceFromIdentity rebuilds the control and FV descriptor from a stored
// identity. version is the profile's client version claim and wins when set:
// the identity keeps the device stable while a profile switch changes only the
// version claim.
func deviceFromIdentity(identity store.DeviceIdentity, version string) apifacade.Device {
	if version == "" {
		version = identity.Version
	}
	return apifacade.Device{
		Site:      identity.Site,
		Version:   version,
		Platform:  identity.OSP,
		OSVersion: identity.OSV,
		OSDevice:  identity.OSD,
		OSHW:      identity.HW,
		DeviceID:  identity.OSDID,
		OSLang:    identity.OSLang,
		TZMinutes: identity.TZMin,
		OSDim:     identity.DIM,
		InstMkt:   identity.InstMkt,
		Lang:      identity.Lang,
		Vrf:       identity.Vrf,
		UID:       identity.UID,
		CUID:      identity.CUID,
		CSEID:     identity.CSEID,
	}
}
