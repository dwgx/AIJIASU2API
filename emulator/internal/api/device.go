package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

// Device holds the common parameters every control API request carries.
//
// Defaults mirror AjsDevice.commonParams() in the public-domain OpenAJS client
// (Android 4.11.10.0). They are configuration, not protocol constants: the
// server may validate _v / cv, and the shipping clients send their own values.
type Device struct {
	Site      string // _site
	Version   string // _v
	Platform  string // _m
	OSVersion string // _os_ver
	OSDevice  string // _os_dev
	OSHW      string // _os_hw
	DeviceID  string // _os_did, 16 lowercase hex chars
	OSLang    string // _os_lang
	TZMinutes int    // _tz, minutes east of UTC
	OSDim     string // _os_dim
	InstMkt   string // _inst_mkt
	Lang      string // _lang
	Vrf       string // _vrf
	UID       string // _uid, 22 chars
}

// DefaultDevice returns the OpenAJS device descriptor with empty generated IDs.
// Call EnsureIDs before building requests.
func DefaultDevice() Device {
	_, offset := time.Now().Zone()
	return Device{
		Site:      "ajs",
		Version:   "4.11.10.0",
		Platform:  "android",
		OSVersion: "13",
		OSDevice:  "Xiaomi 23013RK75C",
		OSHW:      "qcom",
		OSLang:    "zh-Hans-CN",
		TZMinutes: offset / 60,
		OSDim:     "w=1080&h=2400&d=440",
		InstMkt:   "google",
		Lang:      "zh_cn",
		Vrf:       "",
	}
}

// NewDeviceID returns 16 random lowercase hex characters (_os_did / cuid /
// cseid). AjsDevice uses RandomUtil.hex(16) for the same field.
func NewDeviceID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("api: device id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// NewUID builds the 22-character _uid:
//
//	base     = sprintf("%08x%02x%08x", now_ms/1000, now_ms&0xFF, random_int32)
//	checksum = first 4 lowercase hex chars of DualHash(utf8(base))
//	uid      = base + checksum
//
// rnd is the random 32-bit component. Java formats a signed int with %08x,
// which is the two's-complement bit pattern; uint32 produces the same digits.
func NewUID(now time.Time, rnd uint32) string {
	ms := now.UnixMilli()
	base := fmt.Sprintf("%08x%02x%08x", uint32(now.Unix()), byte(ms&0xFF), rnd)
	sum := DualHashHex([]byte(base))
	return base + sum[:4]
}

// EnsureIDs fills DeviceID and UID when they are empty.
func (d *Device) EnsureIDs() error {
	if d.DeviceID == "" {
		id, err := NewDeviceID()
		if err != nil {
			return err
		}
		d.DeviceID = id
	}
	if d.UID == "" {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return fmt.Errorf("api: uid: %w", err)
		}
		rnd := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
		d.UID = NewUID(time.Now(), rnd)
	}
	return nil
}

// CommonParams returns the device parameter map sent with every request.
// The caller adds cmd and endpoint-specific fields, then calls BuildQuery.
func (d Device) CommonParams() map[string]string {
	return map[string]string{
		"_site":     d.Site,
		"_v":        d.Version,
		"_m":        d.Platform,
		"_os_ver":   d.OSVersion,
		"_os_dev":   d.OSDevice,
		"_os_hw":    d.OSHW,
		"_os_did":   d.DeviceID,
		"_os_lang":  d.OSLang,
		"_tz":       strconv.Itoa(d.TZMinutes),
		"_os_dim":   d.OSDim,
		"_inst_mkt": d.InstMkt,
		"_lang":     d.Lang,
		"_vrf":      d.Vrf,
		"_uid":      d.UID,
	}
}
