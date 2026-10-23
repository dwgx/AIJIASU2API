package account

import "ajsproxy/apifacade"

// Client versions of the two supported profiles. LinuxClientVersion is the
// default: the daemon emulates the Linux client unless told otherwise.
const (
	// LinuxClientVersion is the _v value of the Linux 4.2.3.0 client.
	LinuxClientVersion = "4.2.3.0"
	// OpenAJSClientVersion is the _v value of the OpenAJS Android client.
	OpenAJSClientVersion = "4.11.10.0"
)

// NewDevice returns a device descriptor with generated IDs for the given
// client version. An empty version selects LinuxClientVersion; pass
// OpenAJSClientVersion for the OpenAJS/Android profile.
//
// The rest of the descriptor (platform, OS, locale, timezone, screen) comes
// from apifacade.DefaultDevice, which mirrors AjsDevice.commonParams.
func NewDevice(version string) (apifacade.Device, error) {
	if version == "" {
		version = LinuxClientVersion
	}
	device := apifacade.DefaultDevice()
	device.Version = version
	if err := device.EnsureIDs(); err != nil {
		return apifacade.Device{}, err
	}
	return device, nil
}

// DeviceIdentity is the non-secret summary of a device descriptor. It is what
// the daemon stores and logs; the full descriptor never needs to be logged.
type DeviceIdentity struct {
	Version  string // _v
	Platform string // _m
	Model    string // _os_dev
	OSVer    string // _os_ver
	HW       string // _os_hw
	OSDID    string // _os_did, 16 lowercase hex
	UID      string // _uid, 22 characters
	Lang     string // _lang
	TZMin    int    // _tz, minutes east of UTC
	Dim      string // _os_dim
}

// Identify summarizes device for logs and store records. A device descriptor
// carries no credentials, but the summary is still preferred: it is stable and
// small.
func Identify(device apifacade.Device) DeviceIdentity {
	return DeviceIdentity{
		Version:  device.Version,
		Platform: device.Platform,
		Model:    device.OSDevice,
		OSVer:    device.OSVersion,
		HW:       device.OSHW,
		OSDID:    device.DeviceID,
		UID:      device.UID,
		Lang:     device.Lang,
		TZMin:    device.TZMinutes,
		Dim:      device.OSDim,
	}
}
