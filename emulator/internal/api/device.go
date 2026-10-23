package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

// Linux423Version is the _v / v value of the shipping Linux client
// (research/60-dynamic/captures/control.jsonl line 2).
const Linux423Version = "4.2.3.0"

// LinuxCSEID is the cseid the Linux 4.2.3.0 client sends in the FV auth body
// (research/60-dynamic/captures/fvmock-1.log: "cseid=linux").
const LinuxCSEID = "linux"

// linux423OSVersion is the _os_ver / osv value captured from the Linux 4.2.3.0
// client. The value ends with a newline: the control body percent-encodes it as
// %0a, and the FV auth body replaces it with a space (sanitizeAuthValue).
const linux423OSVersion = "Linux version 6.18.33.2-microsoft-standard-WSL2 (root@f1bbfb02316b) " +
	"(gcc (GCC) 13.2.0, GNU ld (GNU Binutils) 2.41) #1 SMP PREEMPT_DYNAMIC " +
	"Thu Jun 18 21:54:43 UTC 2026\n"

// linux423Dim is the _os_dim / dim value captured from the Linux 4.2.3.0
// client. The first component was redacted by the researcher; the second is
// the captured 32-hex machine id.
const linux423Dim = "lid=REDACTED,8e415e603088426aa42fed420212150c"

// Device holds the common parameters every control API request carries.
//
// Defaults mirror AjsDevice.commonParams() in the public-domain OpenAJS client
// (Android 4.11.10.0). They are configuration, not protocol constants: the
// server may validate _v / cv, and the shipping clients send their own values.
type Device struct {
	Site      string // _site
	Version   string // _v, or v in the Linux 4.2.3.0 shape
	Platform  string // _m / osp
	OSVersion string // _os_ver / osv
	OSDevice  string // _os_dev / osd
	OSHW      string // _os_hw
	DeviceID  string // _os_did, 16 lowercase hex chars
	OSLang    string // _os_lang
	TZMinutes int    // _tz, minutes east of UTC
	OSDim     string // _os_dim / dim
	InstMkt   string // _inst_mkt
	Lang      string // _lang
	Vrf       string // _vrf
	UID       string // _uid: 22 chars for OpenAJS, 20 hex for Linux 4.2.3.0
	// CUID and CSEID are the FV auth identity (cuid / cseid); neither is a
	// control-channel parameter. An empty CUID falls back to DeviceID and an
	// empty CSEID falls back to CUID, which is what OpenAJS sends. The Linux
	// 4.2.3.0 client sends a 20-hex cuid and cseid=linux.
	CUID  string
	CSEID string
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

// Linux423Device returns the Linux 4.2.3.0 device descriptor with empty IDs.
// Values come from the captured control request (control.jsonl line 2) and FV
// auth body (fvmock-1.log): _m=linux, _os_dev=x86_64, _lang=en, _site=ajs, the
// captured _os_ver/_os_dim, and cseid=linux. Call EnsureLinuxIDs before use.
func Linux423Device() Device {
	return Device{
		Site:      "ajs",
		Version:   Linux423Version,
		Platform:  "linux",
		OSVersion: linux423OSVersion,
		OSDevice:  "x86_64",
		OSDim:     linux423Dim,
		Lang:      "en",
		CSEID:     LinuxCSEID,
	}
}

// NewLinuxUID returns 20 random lowercase hex characters. The Linux 4.2.3.0
// client sends one 20-hex value as both _uid (control.jsonl line 2) and cuid
// (fvmock-1.log); OpenAJS's 22-character self-checksummed UID is a different
// format.
func NewLinuxUID() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("api: linux uid: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
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

// EnsureLinuxIDs fills the Linux 4.2.3.0 identity when it is empty: _uid and
// cuid are one 20-hex value, cseid is LinuxCSEID, and _os_did is 16 hex.
//
// The Linux control capture carries no _os_did; the field is still generated so
// the identity record is complete and a profile that does send it keeps a
// stable value.
func (d *Device) EnsureLinuxIDs() error {
	if d.UID == "" {
		uid, err := NewLinuxUID()
		if err != nil {
			return err
		}
		d.UID = uid
	}
	if d.CUID == "" {
		d.CUID = d.UID
	}
	if d.CSEID == "" {
		d.CSEID = LinuxCSEID
	}
	if d.DeviceID == "" {
		id, err := NewDeviceID()
		if err != nil {
			return err
		}
		d.DeviceID = id
	}
	return nil
}

// CommonParams returns the OpenAJS/Android device parameter map sent with
// every ShapeOpenAJS request. The caller adds cmd and endpoint-specific
// fields, then calls BuildQuery.
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

// RequestShape selects the control request field set.
type RequestShape int

const (
	// ShapeOpenAJS is the OpenAJS/Android field set: _v, a 22-character
	// self-checksummed _uid, the Android device fields, the okhttp User-Agent
	// and no trailing separator. It is the zero value, so existing callers
	// keep their behaviour.
	ShapeOpenAJS RequestShape = iota
	// ShapeLinux423 is the Linux 4.2.3.0 field set captured in
	// research/60-dynamic/captures/control.jsonl line 2: v instead of _v, a
	// 20-hex _uid, _m/_os_dev/_os_dim/_os_ver/_lang, no Android-only fields,
	// no User-Agent and a trailing "&".
	ShapeLinux423
)

// ControlParams returns the device parameters shape puts on the control
// channel.
func (d Device) ControlParams(shape RequestShape) map[string]string {
	if shape == ShapeLinux423 {
		return d.Linux423Params()
	}
	return d.CommonParams()
}

// Linux423Params returns the Linux 4.2.3.0 common parameters: exactly the
// captured fields, with v instead of _v and without
// _os_hw/_os_did/_os_lang/_tz/_inst_mkt/_vrf.
func (d Device) Linux423Params() map[string]string {
	return map[string]string{
		"_lang":   d.Lang,
		"_m":      d.Platform,
		"_os_dev": d.OSDevice,
		"_os_dim": d.OSDim,
		"_os_ver": d.OSVersion,
		"_site":   d.Site,
		"_uid":    d.UID,
		"v":       d.Version,
	}
}
